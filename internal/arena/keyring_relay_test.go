package arena

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// dbusBody marshals test bodies in little-endian D-Bus format.
type dbusBody struct{ b []byte }

func (e *dbusBody) pad(n int) {
	for len(e.b)%n != 0 {
		e.b = append(e.b, 0)
	}
}
func (e *dbusBody) u32(v uint32) { e.pad(4); e.b = binary.LittleEndian.AppendUint32(e.b, v) }
func (e *dbusBody) str(s string) { e.u32(uint32(len(s))); e.b = append(append(e.b, s...), 0) }
func (e *dbusBody) sig(s string) { e.b = append(append(append(e.b, byte(len(s))), s...), 0) }

// dict writes a{ss} from alternating keys and values.
func (e *dbusBody) dict(kv ...string) {
	e.u32(0)
	at := len(e.b) - 4
	e.pad(8)
	start := len(e.b)
	for i := 0; i < len(kv); i += 2 {
		e.pad(8)
		e.str(kv[i])
		e.str(kv[i+1])
	}
	binary.LittleEndian.PutUint32(e.b[at:], uint32(len(e.b)-start))
}

func (e *dbusBody) paths(paths ...string) {
	e.u32(0)
	at := len(e.b)
	for _, p := range paths {
		e.str(p)
	}
	binary.LittleEndian.PutUint32(e.b[at-4:], uint32(len(e.b)-at))
}

// dbusTestMessage builds a message with string header fields (code -> value,
// typed by code) and an optional body with its signature.
func dbusTestMessage(typ byte, serial uint32, fields map[byte]string, replySerial uint32, signature string, body []byte) []byte {
	h := &dbusBody{}
	types := map[byte]string{1: "o", 2: "s", 3: "s", 4: "s", 6: "s", 7: "s"}
	for _, code := range []byte{1, 2, 3, 4, 6, 7} {
		if v, ok := fields[code]; ok {
			h.pad(8)
			h.b = append(h.b, code)
			h.sig(types[code])
			h.str(v)
		}
	}
	if replySerial != 0 {
		h.pad(8)
		h.b = append(h.b, 5)
		h.sig("u")
		h.u32(replySerial)
	}
	if signature != "" {
		h.pad(8)
		h.b = append(h.b, 8)
		h.sig("g")
		h.sig(signature)
	}
	msg := []byte{'l', typ, 0, 1}
	msg = binary.LittleEndian.AppendUint32(msg, uint32(len(body)))
	msg = binary.LittleEndian.AppendUint32(msg, serial)
	msg = binary.LittleEndian.AppendUint32(msg, uint32(len(h.b)))
	msg = append(msg, h.b...)
	for len(msg)%8 != 0 {
		msg = append(msg, 0)
	}
	return append(msg, body...)
}

func dbusCall(serial uint32, dest, path, iface, member, signature string, body []byte) []byte {
	return dbusTestMessage(dbusMethodCall, serial, map[byte]string{1: path, 2: iface, 3: member, 6: dest}, 0, signature, body)
}

func parseTestMessage(t *testing.T, raw []byte) dbusMessage {
	t.Helper()
	m, err := readDBusMessage(bufio.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func search(serial uint32, kv ...string) []byte {
	body := &dbusBody{}
	body.dict(kv...)
	return dbusCall(serial, secretsName, secretsRoot+"/collection/login", secretsIface+"Collection", "SearchItems", "a{ss}", body.b)
}

func TestKeyringRelayPolicy(t *testing.T) {
	r := &keyringRelay{services: map[string]bool{}, items: map[string]bool{}}
	allowed := func(raw []byte) bool { _, why := r.allow(parseTestMessage(t, raw)); return why == "" }
	for _, call := range [][]byte{
		dbusCall(1, busName, "/org/freedesktop/DBus", busName, "Hello", "", nil),
		dbusCall(2, secretsName, secretsRoot, secretsIface+"Service", "OpenSession", "", nil),
		dbusCall(3, secretsName, secretsRoot, "org.freedesktop.DBus.Properties", "Get", "", nil),
	} {
		if !allowed(call) {
			t.Fatalf("go-keyring needs %v", parseTestMessage(t, call).fields)
		}
	}
	for name, call := range map[string][]byte{
		"systemd-run":         dbusCall(4, "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "StartTransientUnit", "", nil),
		"portal":              dbusCall(5, "org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop", "org.freedesktop.portal.OpenURI", "OpenURI", "", nil),
		"unique name":         dbusCall(6, ":1.42", "/", "org.freedesktop.systemd1.Manager", "StartUnit", "", nil),
		"no destination":      dbusTestMessage(dbusMethodCall, 7, map[byte]string{1: "/", 2: busName, 3: "Hello"}, 0, "", nil),
		"bus ListNames":       dbusCall(8, busName, "/org/freedesktop/DBus", busName, "ListNames", "", nil),
		"become monitor":      dbusCall(9, busName, "/org/freedesktop/DBus", "org.freedesktop.DBus.Monitoring", "BecomeMonitor", "", nil),
		"search everything":   search(10),
		"service GetSecrets":  dbusCall(11, secretsName, secretsRoot, secretsIface+"Service", "GetSecrets", "", nil),
		"unknown item":        dbusCall(12, secretsName, secretsRoot+"/collection/login/7", secretsIface+"Item", "GetSecret", "", nil),
		"collection property": dbusCall(13, secretsName, secretsRoot+"/collection/login", "org.freedesktop.DBus.Properties", "Get", "", nil),
	} {
		if allowed(call) {
			t.Errorf("%s must be refused", name)
		}
	}
	// Before Seal agy's own search teaches the relay its service.
	if !allowed(search(20, "service", "agy-own", "username", "u")) {
		t.Fatal("agy's startup search refused")
	}
	r.Seal()
	if allowed(search(21, "service", "gh:github.com")) {
		t.Fatal("a sealed relay learned a new service")
	}
	if !allowed(search(22, "service", "agy-own")) || r.Reads() != 2 {
		t.Fatalf("learned service refused (reads %d)", r.Reads())
	}
	// Items become reachable only from a forwarded search's reply.
	reply := &dbusBody{}
	reply.paths(secretsRoot + "/collection/login/3")
	r.learnItems(parseTestMessage(t, dbusTestMessage(dbusMethodReturn, 9, nil, 22, "ao", reply.b)), "SearchItems")
	if !allowed(dbusCall(23, secretsName, secretsRoot+"/collection/login/3", secretsIface+"Item", "GetSecret", "", nil)) {
		t.Fatal("agy's own item refused")
	}
	if allowed(dbusCall(24, secretsName, secretsRoot+"/collection/login/4", secretsIface+"Item", "GetSecret", "", nil)) {
		t.Fatal("another item reachable")
	}
	unlock := &dbusBody{}
	unlock.paths(secretsRoot+"/collection/login/3", secretsRoot+"/collection/login/4")
	if allowed(dbusCall(25, secretsName, secretsRoot, secretsIface+"Service", "Unlock", "ao", unlock.b)) {
		t.Fatal("unlocked another item")
	}
}

func TestKeyringRelayDeniedReplyParses(t *testing.T) {
	m := parseTestMessage(t, dbusAccessDenied(77, "no"))
	if m.typ != dbusError || m.u32(5) != 77 || m.str(4) != "org.freedesktop.DBus.Error.AccessDenied" || m.serial == 0 {
		t.Fatalf("denied reply: %+v", m)
	}
	if args, err := m.args(); err != nil || len(args) != 1 || args[0] != "no" {
		t.Fatalf("denied body: %v %v", args, err)
	}
}

func TestDBusDecoderRejectsMalformedInput(t *testing.T) {
	good := search(1, "service", "x")
	for name, raw := range map[string][]byte{
		"byte order": append([]byte{'x'}, good[1:]...),
		"truncated":  good[:len(good)-3],
		"too large":  append(append([]byte{'l', 1, 0, 1}, binary.LittleEndian.AppendUint32(nil, maxRelayMessage+1)...), make([]byte, 8)...),
	} {
		if _, err := readDBusMessage(bufio.NewReader(bytes.NewReader(raw))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, signature := range []string{"", "a", "(s", "{ss)", "z"} {
		if _, _, err := dbusSplitType(signature); err == nil {
			t.Errorf("signature %q accepted", signature)
		}
	}
}

func TestRelayAuthenticateDeclinesFDPassing(t *testing.T) {
	var out bytes.Buffer
	in := bufio.NewReader(strings.NewReader("\x00AUTH EXTERNAL 31303031\r\nNEGOTIATE_UNIX_FD\r\nBEGIN\r\n"))
	if !relayAuthenticate(in, &out) {
		t.Fatal("handshake failed")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\r\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "OK ") || len(lines[0]) != 35 || lines[1] != "ERROR" {
		t.Fatalf("handshake replies %q", lines)
	}
	if relayAuthenticate(bufio.NewReader(strings.NewReader("AUTH EXTERNAL 31\r\n")), io.Discard) {
		t.Fatal("missing NUL byte accepted")
	}
}

func TestSessionBusSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bus")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	for address, want := range map[string]string{
		"unix:path=" + path + ",guid=abc": path,
		"unix:abstract=/tmp/dbus-x":       "",
		"tcp:host=localhost,port=1":       "",
		"unix:path=" + dir + "/missing":   "",
	} {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", address)
		if got := sessionBusSocket(); got != want {
			t.Errorf("%s: got %q", address, got)
		}
	}
}

// TestKeyringRelayEndToEnd runs the relay against a fake bus that answers
// every call it receives, and checks what reaches it.
func TestKeyringRelayEndToEnd(t *testing.T) {
	dir := t.TempDir()
	upstream := filepath.Join(dir, "bus")
	l, err := net.Listen("unix", upstream)
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	reached := make(chan string, 8)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		in := bufio.NewReader(conn)
		if b, _ := in.ReadByte(); b != 0 {
			return
		}
		if line, _ := in.ReadString('\n'); !strings.HasPrefix(line, "AUTH EXTERNAL ") {
			return
		}
		_, _ = io.WriteString(conn, "OK 0123456789abcdef0123456789abcdef\r\n")
		if line, _ := in.ReadString('\n'); line != "BEGIN\r\n" {
			return
		}
		for {
			m, err := readDBusMessage(in)
			if err != nil {
				return
			}
			reached <- m.str(3)
			reply := &dbusBody{}
			reply.paths(secretsRoot + "/collection/login/1")
			_, _ = conn.Write(dbusTestMessage(dbusMethodReturn, 100+m.serial, nil, m.serial, "ao", reply.b))
			// Calls from other peers are never relayed into the sandbox.
			_, _ = conn.Write(dbusCall(900, ":1.5", "/", "org.example", "Ping", "", nil))
		}
	}()
	r, err := startKeyringRelay(upstream)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	client, err := net.Dial("unix", r.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	in := bufio.NewReader(client)
	if _, err := io.WriteString(client, "\x00AUTH EXTERNAL 31\r\nBEGIN\r\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := in.ReadString('\n'); err != nil || !strings.HasPrefix(line, "OK ") {
		t.Fatalf("auth: %q %v", line, err)
	}
	exchange := func(raw []byte) dbusMessage {
		t.Helper()
		if _, err := client.Write(raw); err != nil {
			t.Fatal(err)
		}
		m, err := readDBusMessage(in)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := exchange(search(1, "service", "agy-own")); m.typ != dbusMethodReturn || m.u32(5) != 1 {
		t.Fatalf("search reply: %+v", m)
	}
	if got := <-reached; got != "SearchItems" {
		t.Fatalf("bus saw %q", got)
	}
	r.Seal()
	if m := exchange(dbusCall(2, "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "StartTransientUnit", "", nil)); m.typ != dbusError || m.u32(5) != 2 {
		t.Fatalf("systemd call: %+v", m)
	}
	if m := exchange(dbusCall(3, secretsName, secretsRoot+"/collection/login/1", secretsIface+"Item", "GetSecret", "", nil)); m.typ != dbusMethodReturn || m.u32(5) != 3 {
		t.Fatalf("own item reply: %+v", m)
	}
	if got := <-reached; got != "GetSecret" {
		t.Fatalf("bus saw %q", got)
	}
	select {
	case got := <-reached:
		t.Fatalf("refused call reached the bus: %s", got)
	default:
	}
	r.Close()
	if _, err := os.Stat(r.Socket); err == nil {
		t.Fatal("relay socket outlived Close")
	}
}
