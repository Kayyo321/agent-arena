//go:build darwin || linux

package arena

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// keyringRelay gives a command-fenced Antigravity CLI the one thing it needs
// from the D-Bus session bus: its own sign-in item in the Secret Service
// keyring. agy stores its sign-in there (go-keyring, Secret Service only), so
// hiding the bus signs it out, while passing the bus through would let any
// sandboxed command call other session services (systemd-run --user escapes
// every mount fence) or read every other keyring item.
//
// The relay listens on a private socket that the fence mounts as the session
// bus. Each connection is authenticated by the relay itself and its method
// calls are checked one by one: only the bus's own Hello/match calls and the
// Secret Service calls go-keyring makes are forwarded, and keyring items are
// reachable only through searches for a learned "service" attribute.
//
// Arena never knows which service names agy uses, and never reads a keyring
// item itself. The relay learns them from agy's own searches while agy starts,
// before Arena sends the first turn, and Seal ends learning before any prompt
// (and so any command) exists. Message bodies are parsed only for attributes
// and object paths and are never logged or stored.
type keyringRelay struct {
	dir, Socket, upstream string

	listener net.Listener
	mu       sync.Mutex
	sealed   bool
	services map[string]bool
	items    map[string]bool
	reads    int
	conns    map[net.Conn]bool
	wg       sync.WaitGroup
}

const (
	secretsName     = "org.freedesktop.secrets"
	secretsRoot     = "/org/freedesktop/secrets"
	secretsIface    = "org.freedesktop.Secret."
	busName         = "org.freedesktop.DBus"
	maxRelayMessage = 1 << 20
)

// sessionBusSocket is the path of the user's session bus socket, or "" when
// the bus is absent or only reachable at an abstract address.
func sessionBusSocket() string {
	address := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if address == "" && os.Getenv("XDG_RUNTIME_DIR") != "" {
		address = "unix:path=" + filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "bus")
	}
	for _, candidate := range strings.Split(address, ";") {
		rest, ok := strings.CutPrefix(candidate, "unix:")
		if !ok {
			continue
		}
		for _, kv := range strings.Split(rest, ",") {
			if path, ok := strings.CutPrefix(kv, "path="); ok {
				if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
					return path
				}
			}
		}
	}
	return ""
}

// startKeyringRelay starts a relay to the session bus socket upstream.
func startKeyringRelay(upstream string) (*keyringRelay, error) {
	dir, err := os.MkdirTemp("", "arena-keyring-")
	if err != nil {
		return nil, err
	}
	r := &keyringRelay{dir: dir, Socket: filepath.Join(dir, "bus"), upstream: upstream, services: map[string]bool{}, items: map[string]bool{}, conns: map[net.Conn]bool{}}
	r.listener, err = net.Listen("unix", r.Socket)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			conn, err := r.listener.Accept()
			if err != nil {
				return
			}
			r.mu.Lock()
			r.conns[conn] = true
			r.mu.Unlock()
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.serve(conn)
				r.mu.Lock()
				delete(r.conns, conn)
				r.mu.Unlock()
			}()
		}
	}()
	return r, nil
}

// Seal ends learning: from now on only the services agy searched for while
// starting are reachable.
func (r *keyringRelay) Seal() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

// Reads counts the Secret Service searches the relay forwarded. Arena waits
// for agy's first one before sending a turn.
func (r *keyringRelay) Reads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func (r *keyringRelay) Close() {
	_ = r.listener.Close()
	r.mu.Lock()
	for conn := range r.conns {
		_ = conn.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
	_ = os.RemoveAll(r.dir)
}

func (r *keyringRelay) serve(client net.Conn) {
	defer client.Close()
	in := bufio.NewReader(client)
	if !relayAuthenticate(in, client) {
		return
	}
	bus, err := net.Dial("unix", r.upstream)
	if err != nil {
		return
	}
	defer bus.Close()
	busIn := bufio.NewReader(bus)
	if _, err := io.WriteString(bus, "\x00AUTH EXTERNAL "+hex.EncodeToString([]byte(strconv.Itoa(os.Getuid())))+"\r\n"); err != nil {
		return
	}
	if line, err := busIn.ReadString('\n'); err != nil || !strings.HasPrefix(line, "OK ") {
		return
	}
	if _, err := io.WriteString(bus, "BEGIN\r\n"); err != nil {
		return
	}
	var write sync.Mutex
	toClient := func(b []byte) error { write.Lock(); defer write.Unlock(); _, err := client.Write(b); return err }
	pending := map[uint32]string{} // serial of a forwarded search or create -> member
	var pendingMu sync.Mutex
	done := make(chan struct{}, 2)
	go func() { // bus -> client
		defer func() { done <- struct{}{} }()
		for {
			m, err := readDBusMessage(busIn)
			if err != nil {
				return
			}
			switch m.typ {
			case dbusMethodCall:
				continue // nothing in the sandbox serves calls from other peers
			case dbusSignal:
				if !strings.HasPrefix(m.str(2), secretsIface+"Prompt") && m.str(2) != busName {
					continue
				}
			case dbusMethodReturn:
				pendingMu.Lock()
				member, ok := pending[m.u32(5)]
				delete(pending, m.u32(5))
				pendingMu.Unlock()
				if ok {
					r.learnItems(m, member)
				}
			}
			if toClient(m.raw) != nil {
				return
			}
		}
	}()
	go func() { // client -> bus
		defer func() { done <- struct{}{} }()
		for {
			m, err := readDBusMessage(in)
			if err != nil {
				return
			}
			if m.typ != dbusMethodCall {
				continue
			}
			track, why := r.allow(m)
			if why != "" {
				if m.flags&dbusNoReplyExpected == 0 && toClient(dbusAccessDenied(m.serial, why)) != nil {
					return
				}
				continue
			}
			if track != "" {
				pendingMu.Lock()
				pending[m.serial] = track
				pendingMu.Unlock()
			}
			if _, err := bus.Write(m.raw); err != nil {
				return
			}
		}
	}()
	<-done
}

// relayAuthenticate runs the server side of D-Bus SASL with the sandboxed
// client. Only the relay can reach the real bus, so EXTERNAL is accepted as
// offered, and file-descriptor passing is declined.
func relayAuthenticate(in *bufio.Reader, out io.Writer) bool {
	if b, err := in.ReadByte(); err != nil || b != 0 {
		return false
	}
	reply := func(s string) bool { _, err := io.WriteString(out, s+"\r\n"); return err == nil }
	ok := "OK " + hex.EncodeToString([]byte(ID("")[:16]))
	for range 16 {
		line, err := in.ReadString('\n')
		if err != nil || len(line) > 512 {
			return false
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			return false
		case fields[0] == "AUTH" && len(fields) == 3 && fields[1] == "EXTERNAL", fields[0] == "DATA":
			if !reply(ok) {
				return false
			}
		case fields[0] == "AUTH" && len(fields) == 2 && fields[1] == "EXTERNAL":
			if !reply("DATA") {
				return false
			}
		case fields[0] == "AUTH", fields[0] == "CANCEL", fields[0] == "ERROR":
			if !reply("REJECTED EXTERNAL") {
				return false
			}
		case fields[0] == "BEGIN":
			return true
		default: // NEGOTIATE_UNIX_FD and anything else
			if !reply("ERROR") {
				return false
			}
		}
	}
	return false
}

// allow decides one method call from the sandbox. It returns the member to
// track when the reply names keyring items, or why the call is refused.
func (r *keyringRelay) allow(m dbusMessage) (track, why string) {
	path, iface, member, dest := m.str(1), m.str(2), m.str(3), m.str(6)
	if _, fds := m.fields[9]; fds {
		return "", "file descriptors are not relayed"
	}
	switch dest {
	case busName:
		if iface == busName && Has([]string{"Hello", "AddMatch", "RemoveMatch", "GetNameOwner", "NameHasOwner"}, member) {
			return "", ""
		}
		if iface == busName && member == "StartServiceByName" {
			if args, err := m.args(); err == nil && len(args) > 0 && args[0] == secretsName {
				return "", ""
			}
		}
		return "", "only the Secret Service is reachable from this sandbox"
	case secretsName:
	default:
		return "", "only the Secret Service is reachable from this sandbox"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case path == secretsRoot && iface == secretsIface+"Service" && Has([]string{"OpenSession", "ReadAlias"}, member):
		return "", ""
	case path == secretsRoot && iface == "org.freedesktop.DBus.Properties" && member == "Get":
		return "", ""
	case path == secretsRoot && iface == secretsIface+"Service" && member == "Unlock":
		args, err := m.args()
		if err != nil || len(args) != 1 {
			return "", "malformed Unlock"
		}
		for _, p := range anySlice(args[0]) {
			if s, _ := p.(string); !r.items[s] && !secretsCollection(s) {
				return "", "keyring item outside this sandbox's sign-in"
			}
		}
		return "", ""
	case secretsCollection(path) && iface == secretsIface+"Collection" && (member == "SearchItems" || member == "CreateItem"):
		args, err := m.args()
		if err != nil || len(args) == 0 {
			return "", "malformed " + member
		}
		attributes := args[0]
		if member == "CreateItem" {
			attributes = nil
			for _, entry := range anySlice(args[0]) {
				if kv := anySlice(entry); len(kv) == 2 && kv[0] == secretsIface+"Item.Attributes" {
					if v, ok := kv[1].(dbusVariant); ok {
						attributes = v.value
					}
				}
			}
		}
		service := ""
		for _, entry := range anySlice(attributes) {
			if kv := anySlice(entry); len(kv) == 2 && kv[0] == "service" {
				service, _ = kv[1].(string)
			}
		}
		if service == "" {
			return "", "keyring searches must name a service"
		}
		if !r.services[service] {
			if r.sealed {
				return "", "keyring item outside this sandbox's sign-in"
			}
			r.services[service] = true
		}
		if member == "SearchItems" {
			r.reads++
		}
		return member, ""
	case r.items[path] && iface == secretsIface+"Item" && Has([]string{"GetSecret", "SetSecret", "Delete"}, member):
		return "", ""
	case strings.HasPrefix(path, secretsRoot+"/session/") && iface == secretsIface+"Session" && member == "Close":
		return "", ""
	case strings.HasPrefix(path, secretsRoot+"/prompt/") && iface == secretsIface+"Prompt" && (member == "Prompt" || member == "Dismiss"):
		return "", ""
	}
	return "", "Secret Service call outside this sandbox's sign-in"
}

func secretsCollection(path string) bool {
	for _, prefix := range []string{secretsRoot + "/collection/", secretsRoot + "/aliases/"} {
		if name, ok := strings.CutPrefix(path, prefix); ok && name != "" && !strings.Contains(name, "/") {
			return true
		}
	}
	return false
}

// learnItems records the item paths a forwarded search or create returned.
func (r *keyringRelay) learnItems(m dbusMessage, member string) {
	args, err := m.args()
	if err != nil || len(args) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	add := func(v any) {
		if s, ok := v.(string); ok && strings.HasPrefix(s, secretsRoot+"/") {
			r.items[s] = true
		}
	}
	if member == "CreateItem" {
		add(args[0])
		return
	}
	for _, arg := range args { // Service.SearchItems returns (unlocked, locked)
		for _, p := range anySlice(arg) {
			add(p)
		}
	}
}

// A minimal D-Bus wire codec: enough to frame messages, read header fields
// and decode bodies for the relay's checks.

const (
	dbusMethodCall      = 1
	dbusMethodReturn    = 2
	dbusError           = 3
	dbusSignal          = 4
	dbusNoReplyExpected = 1
)

type dbusMessage struct {
	order  binary.ByteOrder
	typ    byte
	flags  byte
	serial uint32
	fields map[byte]any
	body   []byte
	raw    []byte
}

type dbusVariant struct {
	signature string
	value     any
}

func (m dbusMessage) str(code byte) string {
	if v, ok := m.fields[code].(dbusVariant); ok {
		s, _ := v.value.(string)
		return s
	}
	return ""
}

func (m dbusMessage) u32(code byte) uint32 {
	if v, ok := m.fields[code].(dbusVariant); ok {
		n, _ := v.value.(uint32)
		return n
	}
	return 0
}

func (m dbusMessage) args() ([]any, error) {
	d := &dbusDecoder{order: m.order, buf: m.body}
	values := []any{}
	signature := m.str(8)
	for signature != "" {
		one, rest, err := dbusSplitType(signature)
		if err != nil {
			return nil, err
		}
		v, err := d.value(one, 0)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
		signature = rest
	}
	return values, nil
}

func readDBusMessage(in *bufio.Reader) (dbusMessage, error) {
	head := make([]byte, 16)
	if _, err := io.ReadFull(in, head); err != nil {
		return dbusMessage{}, err
	}
	var order binary.ByteOrder
	switch head[0] {
	case 'l':
		order = binary.LittleEndian
	case 'B':
		order = binary.BigEndian
	default:
		return dbusMessage{}, fmt.Errorf("bad D-Bus byte order")
	}
	if head[3] != 1 {
		return dbusMessage{}, fmt.Errorf("unsupported D-Bus protocol version")
	}
	bodyLen, fieldsLen := order.Uint32(head[4:8]), order.Uint32(head[12:16])
	if bodyLen > maxRelayMessage || fieldsLen > maxRelayMessage {
		return dbusMessage{}, fmt.Errorf("D-Bus message too large")
	}
	headerEnd := (16 + int(fieldsLen) + 7) &^ 7
	raw := make([]byte, headerEnd+int(bodyLen))
	copy(raw, head)
	if _, err := io.ReadFull(in, raw[16:]); err != nil {
		return dbusMessage{}, err
	}
	m := dbusMessage{order: order, typ: head[1], flags: head[2], serial: order.Uint32(head[8:12]), fields: map[byte]any{}, body: raw[headerEnd:], raw: raw}
	d := &dbusDecoder{order: order, buf: raw[:16+int(fieldsLen)], pos: 16}
	for d.pos < len(d.buf) {
		d.align(8)
		code, err := d.byte()
		if err != nil {
			return dbusMessage{}, err
		}
		v, err := d.value("v", 0)
		if err != nil {
			return dbusMessage{}, err
		}
		m.fields[code] = v
	}
	return m, nil
}

// dbusAccessDenied is an AccessDenied error reply to the call with serial.
func dbusAccessDenied(serial uint32, why string) []byte {
	le := binary.LittleEndian
	fields := []byte{}
	field := func(code byte, signature string, value []byte) {
		for len(fields)%8 != 0 {
			fields = append(fields, 0)
		}
		fields = append(fields, code, 1, signature[0], 0)
		fields = append(fields, value...)
	}
	str := func(s string) []byte {
		b := le.AppendUint32(nil, uint32(len(s)))
		return append(append(b, s...), 0)
	}
	field(4, "s", str("org.freedesktop.DBus.Error.AccessDenied"))
	field(5, "u", le.AppendUint32(nil, serial))
	field(8, "g", []byte{1, 's', 0})
	body := str(why)
	msg := []byte{'l', dbusError, dbusNoReplyExpected, 1}
	msg = le.AppendUint32(msg, uint32(len(body)))
	msg = le.AppendUint32(msg, 0xffffffff-serial)
	msg = le.AppendUint32(msg, uint32(len(fields)))
	msg = append(msg, fields...)
	for len(msg)%8 != 0 {
		msg = append(msg, 0)
	}
	return append(msg, body...)
}

// dbusSplitType splits the first complete type off a signature.
func dbusSplitType(signature string) (string, string, error) {
	if signature == "" {
		return "", "", fmt.Errorf("empty D-Bus signature")
	}
	switch signature[0] {
	case 'a':
		inner, rest, err := dbusSplitType(signature[1:])
		return "a" + inner, rest, err
	case '(', '{':
		closing := map[byte]byte{'(': ')', '{': '}'}[signature[0]]
		depth := 0
		for i := 0; i < len(signature); i++ {
			switch signature[i] {
			case '(', '{':
				depth++
			case ')', '}':
				depth--
				if depth == 0 {
					if signature[i] != closing {
						return "", "", fmt.Errorf("bad D-Bus signature")
					}
					return signature[:i+1], signature[i+1:], nil
				}
			}
		}
		return "", "", fmt.Errorf("bad D-Bus signature")
	}
	if !strings.ContainsRune("ybnqiuxtdhsogv", rune(signature[0])) {
		return "", "", fmt.Errorf("bad D-Bus signature")
	}
	return signature[:1], signature[1:], nil
}

type dbusDecoder struct {
	order binary.ByteOrder
	buf   []byte
	pos   int
}

func (d *dbusDecoder) align(n int) { d.pos = (d.pos + n - 1) &^ (n - 1) }

func (d *dbusDecoder) take(n int) ([]byte, error) {
	if n < 0 || d.pos+n > len(d.buf) {
		return nil, fmt.Errorf("truncated D-Bus message")
	}
	b := d.buf[d.pos : d.pos+n]
	d.pos += n
	return b, nil
}

func (d *dbusDecoder) byte() (byte, error) {
	b, err := d.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (d *dbusDecoder) uint32() (uint32, error) {
	d.align(4)
	b, err := d.take(4)
	if err != nil {
		return 0, err
	}
	return d.order.Uint32(b), nil
}

// value decodes one value of a complete type. Containers nest at most 32 deep.
func (d *dbusDecoder) value(signature string, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("D-Bus value nests too deeply")
	}
	switch signature[0] {
	case 'y':
		return d.byte()
	case 'n', 'q':
		d.align(2)
		b, err := d.take(2)
		return b, err
	case 'b', 'i', 'u', 'h':
		return d.uint32()
	case 'x', 't', 'd':
		d.align(8)
		b, err := d.take(8)
		return b, err
	case 's', 'o':
		n, err := d.uint32()
		if err != nil {
			return nil, err
		}
		b, err := d.take(int(n) + 1)
		if err != nil {
			return nil, err
		}
		return string(b[:n]), nil
	case 'g':
		n, err := d.byte()
		if err != nil {
			return nil, err
		}
		b, err := d.take(int(n) + 1)
		if err != nil {
			return nil, err
		}
		return string(b[:n]), nil
	case 'v':
		inner, err := d.value("g", depth+1)
		if err != nil {
			return nil, err
		}
		one, rest, err := dbusSplitType(inner.(string))
		if err != nil || rest != "" {
			return nil, fmt.Errorf("bad D-Bus variant")
		}
		v, err := d.value(one, depth+1)
		return dbusVariant{one, v}, err
	case 'a':
		n, err := d.uint32()
		if err != nil {
			return nil, err
		}
		element := signature[1:]
		switch element[0] {
		case 'x', 't', 'd', '(', '{':
			d.align(8)
		}
		end := d.pos + int(n)
		if n > maxRelayMessage || end > len(d.buf) {
			return nil, fmt.Errorf("truncated D-Bus array")
		}
		items := []any{}
		for d.pos < end {
			v, err := d.value(element, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return items, nil
	case '(', '{':
		d.align(8)
		inner := signature[1 : len(signature)-1]
		items := []any{}
		for inner != "" {
			one, rest, err := dbusSplitType(inner)
			if err != nil {
				return nil, err
			}
			v, err := d.value(one, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, v)
			inner = rest
		}
		return items, nil
	}
	return nil, fmt.Errorf("bad D-Bus signature")
}
