package arena

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
	"github.com/Kayyo321/agent-arena/internal/shader"
)

type Project struct{ Root string }

func Open(dir string) (*Project, error) {
	abs, e := filepath.Abs(dir)
	if e != nil {
		return nil, e
	}
	abs, e = filepath.EvalSymlinks(abs)
	if e != nil {
		return nil, e
	}
	for d := abs; ; d = filepath.Dir(d) {
		if b, e := os.ReadFile(filepath.Join(d, ".arena-root")); e == nil {
			root := strings.TrimSpace(string(b))
			if !filepath.IsAbs(root) {
				return nil, fmt.Errorf("invalid managed worktree control path")
			}
			return &Project{root}, nil
		}
		if _, e := os.Stat(filepath.Join(d, ".arena", "settings.arena.json")); e == nil {
			return &Project{d}, nil
		}
		// A nested Git project must not inherit an enclosing project's arenas.
		if _, e := os.Stat(filepath.Join(d, ".git")); e == nil {
			break
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	b, e := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if e != nil {
		return nil, fmt.Errorf("Arena requires a Git repository: %w", e)
	}
	return &Project{strings.TrimSpace(string(b))}, nil
}
func (p *Project) Path(parts ...string) string {
	return filepath.Join(append([]string{p.Root, ".arena"}, parts...)...)
}
func ID(prefix string) string {
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func Now() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func ReadJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if len(b) > 32<<20 {
		return fmt.Errorf("%s exceeds 32 MiB", path)
	}
	return DecodeJSON(b, v)
}
func Atomic(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".arena-tmp-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func SafePath(base, rel string) (string, error) {
	if info, e := os.Lstat(base); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlink control root is not allowed")
	}
	if filepath.IsAbs(rel) || rel == "" {
		return "", fmt.Errorf("expected relative path")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes control directory")
	}
	cur := base
	for _, part := range strings.Split(clean, string(os.PathSeparator)) {
		cur = filepath.Join(cur, part)
		info, e := os.Lstat(cur)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink not allowed: %s", cur)
		}
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
	}
	return cur, nil
}

type LockOwner struct {
	BootID    string `json:"bootId,omitempty"`
	StartID   string `json:"startId,omitempty"`
	PID       int    `json:"pid"`
	Host      string `json:"host"`
	Token     string `json:"token"`
	CreatedAt string `json:"createdAt"`
}
type Lock struct {
	closed bool
	path   string
	owner  LockOwner
}

func (p *Project) Lock(name string) (*Lock, error) {
	if e := validID(name); e != nil {
		return nil, e
	}
	path := p.Path("locks", name)
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	if e := os.Mkdir(path, 0700); e != nil {
		return nil, fmt.Errorf("%s is locked; inspect arena lock list and use arena lock recover after its owner stops: %w", name, e)
	}
	host, _ := os.Hostname()
	boot, start := ownIdentity()
	l := &Lock{path: path, owner: LockOwner{PID: os.Getpid(), Host: host, Token: ID("owner-"), CreatedAt: Now(), BootID: boot, StartID: start}}
	if e := Atomic(filepath.Join(path, "owner.json"), JSON(l.owner), 0600); e != nil {
		os.Remove(path)
		return nil, e
	}
	return l, nil
}
func (l *Lock) Close() error {
	if l.closed {
		return nil
	}
	var owner LockOwner
	if e := ReadJSON(filepath.Join(l.path, "owner.json"), &owner); e != nil {
		return e
	}
	if owner.Token != l.owner.Token {
		return fmt.Errorf("lock ownership changed")
	}
	if e := os.Remove(filepath.Join(l.path, "owner.json")); e != nil {
		return e
	}
	err := os.Remove(l.path)
	if err == nil {
		l.closed = true
	}
	return err
}
func (p *Project) RecoverLock(name string) error {
	if e := validID(name); e != nil {
		return e
	}
	recovery, err := p.Lock("recover-" + Hash([]byte(name))[:24])
	if err != nil {
		return err
	}
	defer recovery.Close()
	path := p.Path("locks", name)
	var o LockOwner
	if e := ReadJSON(filepath.Join(path, "owner.json"), &o); e != nil {
		return fmt.Errorf("cannot prove owner stopped: %w", e)
	}
	host, _ := os.Hostname()
	if o.Host != host {
		return fmt.Errorf("lock belongs to another host")
	}
	if o.PID < 1 {
		return fmt.Errorf("invalid owner PID")
	}
	boot, start := processIdentity(o.PID)
	reused := o.BootID != "" && boot != "" && o.BootID != boot || o.StartID != "" && start != "" && o.StartID != start
	if err := syscall.Kill(o.PID, 0); !errors.Is(err, syscall.ESRCH) && !reused {
		return fmt.Errorf("owner PID %d may still be alive; refusing recovery", o.PID)
	}
	quarantine := p.Path(".state", "recovered-locks", name+"-"+ID(""))
	if e := os.MkdirAll(filepath.Dir(quarantine), 0700); e != nil {
		return e
	}
	if e := os.Rename(path, quarantine); e != nil {
		return e
	}
	return p.Audit("", "lock.recovered", map[string]any{"lock": name, "owner": o, "preservedAt": quarantine})
}

type Mutation struct {
	Path   string `json:"path"`
	Before []byte `json:"before"`
	After  []byte `json:"after"`
	Delete bool   `json:"delete"`
}
type Transaction struct {
	ID        string     `json:"id"`
	Reason    string     `json:"reason"`
	CreatedAt string     `json:"createdAt"`
	Mutations []Mutation `json:"mutations"`
}

// Commit is called under the project config/context lock. The durable manifest
// precedes file changes, so a crash leaves a reviewable, recoverable transaction.
func (p *Project) Commit(reason string, files map[string][]byte) (string, error) {
	t := Transaction{ID: ID("tx-"), Reason: reason, CreatedAt: Now(), Mutations: []Mutation{}}
	for _, rel := range sortedKeys(files) {
		path, e := SafePath(p.Path(), rel)
		if e != nil {
			return "", e
		}
		before, e := os.ReadFile(path)
		if e != nil && !os.IsNotExist(e) {
			return "", e
		}
		t.Mutations = append(t.Mutations, Mutation{rel, before, files[rel], files[rel] == nil})
	}
	dir := p.Path(".state", "transactions", t.ID)
	if e := Atomic(filepath.Join(dir, "manifest.json"), JSON(t), 0600); e != nil {
		return "", e
	}
	if e := p.applyTransaction(t, false); e != nil {
		return t.ID, e
	}
	return t.ID, Atomic(filepath.Join(dir, "complete"), []byte(Now()), 0600)
}
func (p *Project) applyTransaction(t Transaction, reverse bool) error {
	for _, m := range t.Mutations {
		path, e := SafePath(p.Path(), m.Path)
		if e != nil {
			return e
		}
		b := m.After
		del := m.Delete
		if reverse {
			b = m.Before
			del = b == nil
		}
		current, e := os.ReadFile(path)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if Hash(current) != Hash(m.Before) && Hash(current) != Hash(m.After) {
			return fmt.Errorf("transaction %s conflicts with later edits to %s", t.ID, m.Path)
		}
		if del {
			if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
				return e
			}
		} else if e := Atomic(path, b, 0600); e != nil {
			return e
		}
	}
	return nil
}
func (p *Project) Settings() (Settings, error) {
	var s Settings
	b, e := os.ReadFile(p.Path("settings.arena.json"))
	if e != nil {
		return s, fmt.Errorf("run arena init: %w", e)
	}
	if e = c.ValidateProjectSettings(b); e != nil {
		return s, e
	}
	if e = DecodeJSON(b, &s); e != nil {
		return s, e
	}
	if s.SharedContext == nil {
		d := DefaultSettings(s.DefaultModel)
		s.SharedContext = d.SharedContext
	}
	if err := ValidateSettings(s); err != nil {
		return s, err
	}
	profilePath, err := SafePath(p.Path(), s.PermanentOnCallAgent.Profile)
	if err != nil {
		return s, err
	}
	profileBytes, err := os.ReadFile(profilePath)
	if err != nil {
		return s, fmt.Errorf("permanent orchestrator profile missing; inspect initialization transactions: %w", err)
	}
	if err := c.ValidateProfile(profileBytes); err != nil {
		return s, err
	}
	var profile Profile
	if err := DecodeJSON(profileBytes, &profile); err != nil {
		return s, err
	}
	if profile.Id != "arena-orchestrator" {
		return s, fmt.Errorf("permanent orchestrator profile ID mismatch")
	}
	return s, nil
}
func (p *Project) Load(id string) (Workflow, map[string]Profile, error) {
	w, profiles, _, err := p.LoadVersion(id)
	return w, profiles, err
}
func (p *Project) LoadVersion(id string) (Workflow, map[string]Profile, string, error) {
	l, err := p.WaitLock("config")
	if err != nil {
		return Workflow{}, nil, "", err
	}
	defer l.Close()
	w, profiles, err := p.loadUnlocked(id)
	if err != nil {
		return w, profiles, "", err
	}
	version, err := p.configFingerprint(id)
	return w, profiles, version, err
}
func (p *Project) loadUnlocked(id string) (Workflow, map[string]Profile, error) {
	var w Workflow
	profiles := map[string]Profile{}
	if e := validID(id); e != nil {
		return w, profiles, e
	}
	b, e := os.ReadFile(p.Path(id + ".arena.json"))
	if e != nil {
		return w, profiles, e
	}
	if e = c.ValidateWorkflow(b); e != nil {
		return w, profiles, e
	}
	if e = DecodeJSON(b, &w); e != nil {
		return w, profiles, e
	}
	if w.Id != id {
		return w, profiles, fmt.Errorf("workflow file/ID mismatch")
	}
	for _, a := range w.Agents {
		path, e := SafePath(p.Path(), a.Profile)
		if e != nil {
			return w, profiles, e
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return w, profiles, e
		}
		if e = c.ValidateProfile(b); e != nil {
			return w, profiles, e
		}
		var profile Profile
		if e = DecodeJSON(b, &profile); e != nil {
			return w, profiles, e
		}
		profiles[a.Id] = profile
	}
	s, e := p.Settings()
	if e != nil {
		return w, profiles, e
	}
	return w, profiles, ValidateWorkflow(w, profiles, s)
}
func (p *Project) List() ([]string, error) {
	files, e := filepath.Glob(p.Path("*.arena.json"))
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".arena.json")
		if id != "settings" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (p *Project) Save(w Workflow, profiles map[string]Profile, expected string) (string, error) {
	return p.saveWithShader(w, profiles, expected, nil)
}
func (p *Project) saveWithShader(w Workflow, profiles map[string]Profile, expected string, source *string) (string, error) {
	l, e := p.WaitLock("config")
	if e != nil {
		return "", e
	}
	defer l.Close()
	s, e := p.Settings()
	if e != nil {
		return "", e
	}
	if e = ValidateWorkflow(w, profiles, s); e != nil {
		return "", e
	}
	current, e := os.ReadFile(p.Path(w.Id + ".arena.json"))
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	if expected == "" && current != nil {
		return "", fmt.Errorf("workflow already exists")
	}
	if expected != "" {
		version, err := p.configFingerprint(w.Id)
		if err != nil {
			return "", err
		}
		if version != expected {
			return "", fmt.Errorf("workflow, profile or settings changed during editing; reload before saving")
		}
	}
	if current == nil && w.Settings.BackgroundShader == nil {
		w.Settings.BackgroundShader = defaultShader(w.Id)
		if _, err := os.Lstat(p.Path(w.Settings.BackgroundShader.Source)); err == nil {
			return "", fmt.Errorf("shader source already exists; preserve it by explicitly selecting it in the workflow builder request")
		} else if !os.IsNotExist(err) {
			return "", err
		}
		value := shader.DefaultSource
		source = &value
	}
	if source != nil {
		if _, err := shader.Compile(*source); err != nil {
			return "", err
		}
	}
	if e = ValidateWorkflow(w, profiles, s); e != nil {
		return "", e
	}
	files := map[string][]byte{w.Id + ".arena.json": JSON(w)}
	if source != nil {
		files[w.Settings.BackgroundShader.Source] = []byte(*source)
	}
	if source == nil && w.Settings.BackgroundShader != nil && w.Settings.BackgroundShader.Enabled {
		text, err := p.shaderSource(w)
		if err != nil {
			return "", err
		}
		if _, err = shader.Compile(text); err != nil {
			return "", err
		}
	}
	for _, a := range w.Agents {
		files[a.Profile] = JSON(profiles[a.Id])
	}
	id, e := p.Commit("save workflow "+w.Id, files)
	if e == nil {
		_, e = p.ContextDir(w.Id)
	}
	return id, e
}
func sortedKeys[V any](m map[string]V) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
func sortStrings(s []string) { sort.Strings(s) }

func (p *Project) WaitLock(name string) (*Lock, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		l, err := p.Lock(name)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, os.ErrExist) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (p *Project) configFingerprint(id string) (string, error) {
	paths := []string{id + ".arena.json", "settings.arena.json"}
	var w Workflow
	if err := ReadJSON(p.Path(id+".arena.json"), &w); err != nil {
		return "", err
	}
	for _, a := range w.Agents {
		paths = append(paths, a.Profile)
	}
	if w.Settings.BackgroundShader != nil {
		paths = append(paths, w.Settings.BackgroundShader.Source)
	}
	sort.Strings(paths)
	var combined strings.Builder
	for _, rel := range paths {
		path, err := SafePath(p.Path(), rel)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(path)
		if err != nil && !(os.IsNotExist(err) && strings.HasSuffix(rel, ".background.hlsl")) {
			return "", err
		}
		combined.WriteString(rel)
		combined.WriteString(Hash(b))
	}
	return Hash([]byte(combined.String())), nil
}
