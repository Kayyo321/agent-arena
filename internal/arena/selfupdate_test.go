package arena

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCompareSemver(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.0-dev", "0.1.0", -1},
		{"0.1.0", "0.1.0-dev", 1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "1.0.0", 0},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"10.0.0", "9.9.9", 1},
	} {
		a, okA := parseSemver(c.a)
		b, okB := parseSemver(c.b)
		if !okA || !okB {
			t.Fatalf("parse %q %q: %v %v", c.a, c.b, okA, okB)
		}
		if got := compareSemver(a, b); got != c.want {
			t.Errorf("compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if !versionNewer("0.2.0", "0.1.0-dev") || versionNewer("0.1.0-dev", "0.1.0") || versionNewer("junk", "0.1.0") {
		t.Fatal("versionNewer ordering is wrong")
	}
}

func TestParseSemverMatchesTheInstallerAndTagRule(t *testing.T) {
	for _, ok := range []string{"0.1.0-dev", "0.2.0", "10.20.30", "1.0.0-rc.1"} {
		if _, got := parseSemver(ok); !got {
			t.Errorf("parseSemver(%q) must be accepted", ok)
		}
	}
	for _, bad := range []string{"0.2", "v0.2.0", "../x", "1.2.3+build", "1.2.3-", "1.2.3-a+b", "1.2.3 ", "", "99999999999999999999.0.0"} {
		if _, got := parseSemver(bad); got {
			t.Errorf("parseSemver(%q) must be rejected", bad)
		}
	}
	if v, _ := parseSemver("1.2.3-beta.2"); v.core != [3]int{1, 2, 3} || strings.Join(v.pre, "|") != "beta|2" {
		t.Errorf("parseSemver(1.2.3-beta.2) = %+v", v)
	}
}

func TestNormalizeVersion(t *testing.T) {
	if v, err := normalizeVersion("v1.2.3"); err != nil || v != "1.2.3" {
		t.Fatalf("normalizeVersion(v1.2.3) = %q, %v", v, err)
	}
	for _, bad := range []string{"", "1.2", "../1.2.3", "1.2.3/../x", "latest", "1.2.3+build"} {
		if _, err := normalizeVersion(bad); err == nil {
			t.Errorf("normalizeVersion(%q) must fail", bad)
		}
	}
}

func TestManagedInstallAt(t *testing.T) {
	in, err := managedInstallAt("/opt/arena/lib/arena/0.2.0/arena")
	if err != nil || in.Prefix != "/opt/arena" || in.Version != "0.2.0" || in.Store != "/opt/arena/lib/arena/0.2.0/arena" {
		t.Fatalf("managedInstallAt: %+v %v", in, err)
	}
	for _, exe := range []string{"/repo/bin/arena", "/usr/lib/arena/tools/arena", "/p/lib/arena/0.2.0/other", "/p/share/arena/0.2.0/arena"} {
		if _, err := managedInstallAt(exe); err == nil {
			t.Errorf("%s must not count as a managed install", exe)
		}
	}
}

// releaseServer serves /latest as a redirect and any other path from files.
type releaseServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newReleaseServer(t *testing.T, latest string, files map[string][]byte) *releaseServer {
	t.Helper()
	s := &releaseServer{hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		if r.URL.Path == "/latest" && latest != "" {
			http.Redirect(w, r, "/tag/v"+latest, http.StatusFound)
			return
		}
		if body, ok := files[r.URL.Path]; ok {
			w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	t.Setenv("ARENA_RELEASE_BASE_URL", s.URL)
	return s
}

func (s *releaseServer) hitCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func fakeBinary(version string) string { return "#!/bin/sh\necho \"arena " + version + "\"\n" }

func releaseArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := int64(0o644)
		if name == "arena" {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(files[name])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseFiles is the archive and SHA256SUMS a release of version serves for this platform.
func releaseFiles(t *testing.T, version, binary, skill string) map[string][]byte {
	t.Helper()
	goos, goarch, err := releasePlatform()
	if err != nil {
		t.Skip(err)
	}
	name := fmt.Sprintf("arena_%s_%s_%s.tar.gz", version, goos, goarch)
	archive := releaseArchive(t, map[string]string{"arena": binary, "README.md": "readme", releaseSkillPath: skill})
	dir := "/download/v" + version + "/"
	return map[string][]byte{
		dir + name:         archive,
		dir + "SHA256SUMS": []byte("0000  arena_" + version + "_source.tar.gz\n" + Hash(archive) + "  " + name + "\n"),
	}
}

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

type managedEnv struct {
	prefix, home, project, exe string
}

// newManagedEnv lays out <prefix>/lib/arena/<version>/arena with bin/arena linked to it,
// plus a temp HOME, state dir and a project that has an .arena/ directory.
func newManagedEnv(t *testing.T, version string) managedEnv {
	t.Helper()
	e := managedEnv{prefix: resolvedTemp(t), home: resolvedTemp(t), project: resolvedTemp(t)}
	t.Setenv("HOME", e.home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(e.home, "state"))
	e.exe = filepath.Join(e.prefix, "lib", "arena", version, "arena")
	writeFile(t, e.exe, fakeBinary(version), 0o755)
	if err := os.MkdirAll(filepath.Join(e.prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.exe, filepath.Join(e.prefix, "bin", "arena")); err != nil {
		t.Fatal(err)
	}
	e.writeRecord(t, validRecord(e.prefix, version, e.exe))
	writeFile(t, filepath.Join(e.project, ".arena", "settings.arena.json"), `{"keep":"me"}`, 0o644)
	writeFile(t, filepath.Join(e.project, ".gitignore"), ".arena/.state/\n", 0o644)
	return e
}

func (e managedEnv) link() string { return filepath.Join(e.prefix, "bin", "arena") }

func validRecord(prefix, version, store string) installRecord {
	return installRecord{SchemaVersion: 1, Prefix: prefix, Version: version, StorePath: store, BinarySHA256: Hash([]byte(fakeBinary(version))), Source: "release", InstalledAt: "2026-10-17T09:00:00Z"}
}

func (e managedEnv) writeRecord(t *testing.T, rec installRecord) {
	t.Helper()
	writeFile(t, filepath.Join(e.prefix, "share", "arena", "install.json"), string(JSON(rec)), 0o644)
}

// snapshot describes every entry under root (type, symlink target, content hash)
// so a test can prove a refused command changed nothing.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			fmt.Fprintf(&b, "%s -> %s\n", rel, target)
		case info.IsDir():
			fmt.Fprintf(&b, "%s/\n", rel)
		default:
			content, _ := os.ReadFile(path)
			fmt.Fprintf(&b, "%s %s\n", rel, Hash(content))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func (e managedEnv) linkTarget(t *testing.T) string {
	t.Helper()
	target, err := os.Readlink(e.link())
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func testCLI() (*CLI, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return NewCLI(strings.NewReader(""), &out, &errOut), &out, &errOut
}

func yes(extra ...string) Options {
	o := Options{"yes": {"true"}}
	for i := 0; i+1 < len(extra); i += 2 {
		o[extra[i]] = []string{extra[i+1]}
	}
	return o
}

func TestLatestReleaseFollowsRedirectWithoutFetchingTheTagPage(t *testing.T) {
	s := newReleaseServer(t, "1.2.3", nil)
	got, err := latestRelease(context.Background(), s.URL)
	if err != nil || got != "1.2.3" {
		t.Fatalf("latestRelease = %q, %v", got, err)
	}
	if s.hitCount("/tag/v1.2.3") != 0 {
		t.Fatal("the tag page must not be fetched")
	}
	empty := newReleaseServer(t, "", nil)
	if _, err := latestRelease(context.Background(), empty.URL); err == nil {
		t.Fatal("a missing latest release must be an error")
	}
}

func TestUpdateInstallsNewerReleaseAndSwitchesLink(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	skillPath := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
	editedPath := filepath.Join(e.home, ".agents", "skills", "arena-orchestrator", "SKILL.md")
	writeFile(t, skillPath, "old skill", 0o644)
	writeFile(t, editedPath, "my edits", 0o644)
	if err := recordUserSkill(skillPath, []byte("old skill")); err != nil {
		t.Fatal(err)
	}
	if err := recordUserSkill(editedPath, []byte("old skill")); err != nil {
		t.Fatal(err)
	}
	files := releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "new skill")
	newReleaseServer(t, "0.2.0", files)

	cli, out, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, yes()); err != nil {
		t.Fatalf("update: %v", err)
	}
	store := filepath.Join(e.prefix, "lib", "arena", "0.2.0", "arena")
	if got := e.linkTarget(t); got != store {
		t.Fatalf("bin/arena -> %q, want %q", got, store)
	}
	if info, err := os.Stat(store); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("store binary: %v %v", info, err)
	}
	if _, err := os.Stat(e.exe); err != nil {
		t.Fatalf("the previous version must stay in the store: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(e.prefix, "bin"))
	if len(entries) != 1 {
		t.Fatalf("bin/ holds leftovers: %v", entries)
	}
	var rec installRecord
	if err := ReadJSON(filepath.Join(e.prefix, "share", "arena", "install.json"), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.SchemaVersion != 1 || rec.Version != "0.2.0" || rec.Prefix != e.prefix || rec.StorePath != store || rec.Source != "release" || rec.BinarySHA256 != Hash([]byte(fakeBinary("0.2.0"))) {
		t.Fatalf("install.json: %+v", rec)
	}
	if _, err := time.Parse(time.RFC3339, rec.InstalledAt); err != nil {
		t.Fatalf("installedAt: %v", err)
	}
	if !strings.Contains(out.String(), "0.2.0") {
		t.Fatalf("output must name the new version: %q", out.String())
	}
	if !strings.Contains(out.String(), skillPath) || !strings.Contains(out.String(), "arena skill install") {
		t.Fatalf("output must advise refreshing the unmodified skill copy: %q", out.String())
	}
	if strings.Contains(out.String(), editedPath) {
		t.Fatalf("an edited skill copy must not be advised for replacement: %q", out.String())
	}
}

func TestUpdateRefusesChecksumMismatch(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	files := releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "skill")
	files["/download/v0.2.0/SHA256SUMS"] = []byte(strings.Repeat("0", 64) + "  " + archiveName(t, "0.2.0") + "\n")
	newReleaseServer(t, "0.2.0", files)

	cli, _, _ := testCLI()
	err := cli.update(context.Background(), e.exe, e.project, yes())
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a mismatched checksum must be refused, got %v", err)
	}
	if got := e.linkTarget(t); got != e.exe {
		t.Fatalf("the link must not move, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.prefix, "lib", "arena", "0.2.0")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be stored for a rejected archive: %v", err)
	}
}

func archiveName(t *testing.T, version string) string {
	t.Helper()
	goos, goarch, err := releasePlatform()
	if err != nil {
		t.Skip(err)
	}
	return fmt.Sprintf("arena_%s_%s_%s.tar.gz", version, goos, goarch)
}

func TestUpdateRefusesArchiveMissingFromChecksums(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	files := releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "skill")
	files["/download/v0.2.0/SHA256SUMS"] = []byte("abcd  something_else.tar.gz\n")
	newReleaseServer(t, "0.2.0", files)
	cli, _, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, yes()); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Fatalf("an archive without a checksum line must be refused, got %v", err)
	}
	if got := e.linkTarget(t); got != e.exe {
		t.Fatalf("the link must not move, got %q", got)
	}
}

func TestUpdateRefusesDowngradeWithoutTo(t *testing.T) {
	setVersion(t, "0.3.0")
	e := newManagedEnv(t, "0.3.0")
	files := releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "skill")
	s := newReleaseServer(t, "0.2.0", files)

	cli, out, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, yes()); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out.String(), "--to 0.2.0") {
		t.Fatalf("output must point at --to: %q", out.String())
	}
	if got := e.linkTarget(t); got != e.exe {
		t.Fatalf("the link must not move, got %q", got)
	}
	if n := s.hitCount("/download/v0.2.0/" + archiveName(t, "0.2.0")); n != 0 {
		t.Fatalf("nothing may be downloaded for a refused downgrade, got %d requests", n)
	}

	// --to is the explicit opt-in.
	out.Reset()
	if err := cli.update(context.Background(), e.exe, e.project, yes("to", "v0.2.0")); err != nil {
		t.Fatalf("update --to: %v", err)
	}
	if got, want := e.linkTarget(t), filepath.Join(e.prefix, "lib", "arena", "0.2.0", "arena"); got != want {
		t.Fatalf("bin/arena -> %q, want %q", got, want)
	}
}

func TestUpdateCheckChangesNothing(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	files := releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "skill")
	s := newReleaseServer(t, "0.2.0", files)

	cli, out, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, Options{"check": {"true"}}); err != nil {
		t.Fatalf("update --check: %v", err)
	}
	if !strings.Contains(out.String(), "0.1.0") || !strings.Contains(out.String(), "0.2.0") {
		t.Fatalf("--check must print current and latest: %q", out.String())
	}
	if got := e.linkTarget(t); got != e.exe {
		t.Fatalf("the link must not move, got %q", got)
	}
	if s.hitCount("/download/v0.2.0/"+archiveName(t, "0.2.0")) != 0 {
		t.Fatal("--check must not download")
	}
	if err := cli.update(context.Background(), e.exe, e.project, Options{"check": {"true"}, "to": {"0.2.0"}}); err == nil {
		t.Fatal("--check with --to must be rejected")
	}
}

func TestUpdateRemovesABinaryThatFailsItsSmokeTest(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	files := releaseFiles(t, "0.2.0", fakeBinary("9.9.9"), "skill")
	newReleaseServer(t, "0.2.0", files)
	cli, _, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, yes()); err == nil {
		t.Fatal("a binary that reports another version must be refused")
	}
	if got := e.linkTarget(t); got != e.exe {
		t.Fatalf("the link must not move, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.prefix, "lib", "arena", "0.2.0")); !os.IsNotExist(err) {
		t.Fatalf("the rejected binary must not stay in the store: %v", err)
	}
}

func TestUpdateReusesAVersionAlreadyInTheStore(t *testing.T) {
	setVersion(t, "0.2.0")
	e := newManagedEnv(t, "0.2.0")
	old := filepath.Join(e.prefix, "lib", "arena", "0.1.0", "arena")
	writeFile(t, old, fakeBinary("0.1.0"), 0o755)
	s := newReleaseServer(t, "0.2.0", nil)

	// The reused version's skill was not downloaded, so the old binary's skill must
	// not be used to call a tracked copy stale.
	copyPath := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
	writeFile(t, copyPath, "older skill", 0o644)
	if err := recordUserSkill(copyPath, []byte("older skill")); err != nil {
		t.Fatal(err)
	}

	cli, out, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, yes("to", "0.1.0")); err != nil {
		t.Fatalf("update --to: %v", err)
	}
	if got := e.linkTarget(t); got != old {
		t.Fatalf("bin/arena -> %q, want %q", got, old)
	}
	if !strings.Contains(out.String(), "already in the store, so its skill was not compared") || strings.Contains(out.String(), copyPath) {
		t.Fatalf("a reused version must skip the stale-skill list and say so: %q", out.String())
	}
	for path, n := range s.hits {
		if strings.HasPrefix(path, "/download/") && n > 0 {
			t.Fatalf("a stored version must not be downloaded again: %s", path)
		}
	}
}

func TestUpdateAndUninstallRefuseUnmanagedBinaries(t *testing.T) {
	cli, _, _ := testCLI()
	if err := cli.update(context.Background(), "/repo/bin/arena", t.TempDir(), yes()); err == nil || !strings.Contains(err.Error(), "installer-managed") {
		t.Fatalf("update on a repo binary: %v", err)
	}
	if err := cli.uninstall(context.Background(), "/repo/bin/arena", t.TempDir(), yes()); err == nil || !strings.Contains(err.Error(), "installer-managed") {
		t.Fatalf("uninstall on a repo binary: %v", err)
	}
	// Through Run, the test binary is not a managed install either. This also
	// proves the new flags parse and the value of --to is not left behind.
	for _, args := range [][]string{{"update", "--check"}, {"update", "--to", "1.0.0", "--yes"}, {"uninstall", "--dry-run", "--force"}} {
		cli, _, _ := testCLI()
		err := cli.Run(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "installer-managed") {
			t.Errorf("arena %v: %v", args, err)
		}
	}
}

func TestUninstallKeepsProjectStateAndModifiedSkillCopies(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	pristine := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
	edited := filepath.Join(e.home, ".agents", "skills", "arena-orchestrator", "SKILL.md")
	writeFile(t, pristine, "installed", 0o644)
	writeFile(t, edited, "installed", 0o644)
	for _, p := range []string{pristine, edited} {
		if err := recordUserSkill(p, []byte("installed")); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, edited, "installed, then edited", 0o644)
	writeFile(t, filepath.Join(e.project, ".arena", "demo.arena.json"), `{"workflow":true}`, 0o644)
	writeFile(t, filepath.Join(e.project, ".arena", ".state", "run-1", "run.json"), `{}`, 0o600)

	cli, out, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, yes()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, gone := range []string{e.link(), filepath.Join(e.prefix, "lib", "arena"), filepath.Join(e.prefix, "share", "arena", "install.json"), pristine, filepath.Join(e.home, "state", "arena", "user-skills.json")} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should be gone: %v", gone, err)
		}
	}
	if b, err := os.ReadFile(edited); err != nil || string(b) != "installed, then edited" {
		t.Fatalf("a modified skill copy must be kept untouched: %q %v", b, err)
	}
	for path, want := range map[string]string{
		filepath.Join(e.project, ".arena", "settings.arena.json"):         `{"keep":"me"}`,
		filepath.Join(e.project, ".arena", "demo.arena.json"):             `{"workflow":true}`,
		filepath.Join(e.project, ".arena", ".state", "run-1", "run.json"): `{}`,
		filepath.Join(e.project, ".gitignore"):                            ".arena/.state/\n",
	} {
		if b, err := os.ReadFile(path); err != nil || string(b) != want {
			t.Errorf("%s must be untouched: %q %v", path, b, err)
		}
	}
	for _, want := range []string{"Left in place", filepath.Join(e.project, ".arena"), edited, ".gitignore"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("checklist lacks %q:\n%s", want, out.String())
		}
	}
}

func TestUninstallForceBacksUpModifiedSkillCopies(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	edited := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
	writeFile(t, edited, "mine", 0o644)
	if err := recordUserSkill(edited, []byte("installed")); err != nil {
		t.Fatal(err)
	}
	cli, _, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, yes("force", "true")); err != nil {
		t.Fatalf("uninstall --force: %v", err)
	}
	if _, err := os.Lstat(edited); !os.IsNotExist(err) {
		t.Fatalf("--force removes the modified copy: %v", err)
	}
	backups, _ := filepath.Glob(filepath.Join(e.home, "state", "arena", "skill-backups", "*.md"))
	if len(backups) != 1 {
		t.Fatalf("expected one backup, got %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != "mine" {
		t.Fatalf("backup content = %q", b)
	}
}

func TestUninstallDryRunChangesNothing(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	skill := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
	writeFile(t, skill, "installed", 0o644)
	if err := recordUserSkill(skill, []byte("installed")); err != nil {
		t.Fatal(err)
	}
	cli, out, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, Options{"dry-run": {"true"}}); err != nil {
		t.Fatalf("uninstall --dry-run: %v", err)
	}
	for _, still := range []string{e.exe, e.link(), skill, filepath.Join(e.prefix, "share", "arena", "install.json"), filepath.Join(e.home, "state", "arena", "user-skills.json")} {
		if _, err := os.Lstat(still); err != nil {
			t.Errorf("dry run removed %s: %v", still, err)
		}
	}
	if !strings.Contains(out.String(), "Dry run") || !strings.Contains(out.String(), e.link()) {
		t.Fatalf("dry run must print the plan: %q", out.String())
	}
}

func TestUninstallRefusesWhileARunIsLiveUnlessForced(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	p := &Project{Root: e.project}
	if err := os.MkdirAll(p.Path(".state", "run-live"), 0o700); err != nil {
		t.Fatal(err)
	}
	// This process owns the run lock, so RunAlive reports the run live.
	lock, err := p.Lock("run-run-live")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if !RunAlive(p, "run-live") {
		t.Fatal("test setup: the run should be live")
	}

	cli, _, _ := testCLI()
	err = cli.uninstall(context.Background(), e.exe, e.project, yes())
	if err == nil || !strings.Contains(err.Error(), "run-live") {
		t.Fatalf("uninstall must refuse while a run is live, got %v", err)
	}
	if _, err := os.Lstat(e.link()); err != nil {
		t.Fatalf("a refused uninstall removed the link: %v", err)
	}
	if err := cli.uninstall(context.Background(), e.exe, e.project, yes("force", "true")); err != nil {
		t.Fatalf("uninstall --force: %v", err)
	}
	if _, err := os.Lstat(e.link()); !os.IsNotExist(err) {
		t.Fatalf("the link should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.project, ".arena", "locks", "run-run-live", "owner.json")); err != nil {
		t.Fatalf("uninstall must leave the project's .arena/ alone: %v", err)
	}
}

func TestUpdateAndUninstallRefuseUnverifiedInstalls(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(t *testing.T, e managedEnv)
	}{
		{"no install.json", func(t *testing.T, e managedEnv) {
			if err := os.Remove(filepath.Join(e.prefix, "share", "arena", "install.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"unparsable install.json", func(t *testing.T, e managedEnv) {
			writeFile(t, filepath.Join(e.prefix, "share", "arena", "install.json"), "{not json", 0o644)
		}},
		{"unknown schema", func(t *testing.T, e managedEnv) {
			rec := validRecord(e.prefix, "0.1.0", e.exe)
			rec.SchemaVersion = 2
			e.writeRecord(t, rec)
		}},
		{"manifest names another prefix", func(t *testing.T, e managedEnv) {
			e.writeRecord(t, validRecord(resolvedTemp(t), "0.1.0", e.exe))
		}},
		{"manifest has no prefix", func(t *testing.T, e managedEnv) {
			e.writeRecord(t, validRecord("", "0.1.0", e.exe))
		}},
		{"manifest names another binary", func(t *testing.T, e managedEnv) {
			other := filepath.Join(resolvedTemp(t), "arena")
			writeFile(t, other, fakeBinary("0.1.0"), 0o755)
			e.writeRecord(t, validRecord(e.prefix, "0.1.0", other))
		}},
		{"manifest binary is gone", func(t *testing.T, e managedEnv) {
			e.writeRecord(t, validRecord(e.prefix, "0.1.0", filepath.Join(e.prefix, "lib", "arena", "0.0.1", "arena")))
		}},
		{"bin/arena is missing", func(t *testing.T, e managedEnv) {
			if err := os.Remove(e.link()); err != nil {
				t.Fatal(err)
			}
		}},
		{"bin/arena is a plain file", func(t *testing.T, e managedEnv) {
			if err := os.Remove(e.link()); err != nil {
				t.Fatal(err)
			}
			writeFile(t, e.link(), fakeBinary("0.1.0"), 0o755)
		}},
		{"bin/arena points elsewhere", func(t *testing.T, e managedEnv) {
			other := filepath.Join(resolvedTemp(t), "arena")
			writeFile(t, other, "elsewhere", 0o755)
			if err := os.Remove(e.link()); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, e.link()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			setVersion(t, "0.1.0")
			e := newManagedEnv(t, "0.1.0")
			s := newReleaseServer(t, "0.2.0", releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "skill"))
			c.mutate(t, e)
			before := snapshot(t, e.prefix)
			cli, _, _ := testCLI()
			for name, run := range map[string]func() error{
				"update":    func() error { return cli.update(context.Background(), e.exe, e.project, yes()) },
				"uninstall": func() error { return cli.uninstall(context.Background(), e.exe, e.project, yes("force", "true")) },
			} {
				if err := run(); err == nil || !strings.Contains(err.Error(), "not a verified install") {
					t.Errorf("%s must refuse, got %v", name, err)
				}
			}
			if after := snapshot(t, e.prefix); after != before {
				t.Fatalf("a refused command changed the prefix:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if n := s.hitCount("/latest"); n != 0 {
				t.Fatalf("a refused update must not look up releases, got %d requests", n)
			}
		})
	}
}

// With <prefix>/lib symlinked elsewhere, the running binary resolves to
// <shared>/lib/arena/<ver>/arena and the derived prefix is <shared>, which the
// installer never set up. Nothing may be created or removed anywhere.
func TestSymlinkedLibIsRefusedAndNothingIsWrittenOutsideThePrefix(t *testing.T) {
	setVersion(t, "0.1.0")
	prefix, shared, home := resolvedTemp(t), resolvedTemp(t), resolvedTemp(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	writeFile(t, filepath.Join(shared, "lib", "arena", "0.1.0", "arena"), fakeBinary("0.1.0"), 0o755)
	if err := os.Symlink(filepath.Join(shared, "lib"), filepath.Join(prefix, "lib")); err != nil {
		t.Fatal(err)
	}
	viaPrefix := filepath.Join(prefix, "lib", "arena", "0.1.0", "arena")
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(viaPrefix, filepath.Join(prefix, "bin", "arena")); err != nil {
		t.Fatal(err)
	}
	e := managedEnv{prefix: prefix}
	e.writeRecord(t, validRecord(prefix, "0.1.0", viaPrefix))
	exe, err := filepath.EvalSymlinks(filepath.Join(prefix, "bin", "arena"))
	if err != nil || exe != filepath.Join(shared, "lib", "arena", "0.1.0", "arena") {
		t.Fatalf("test setup: the running binary resolves to %q, %v", exe, err)
	}
	newReleaseServer(t, "0.2.0", releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "skill"))
	project := resolvedTemp(t)
	before := map[string]string{"prefix": snapshot(t, prefix), "shared": snapshot(t, shared)}

	cli, _, _ := testCLI()
	if err := cli.update(context.Background(), exe, project, yes()); err == nil || !strings.Contains(err.Error(), "install.json") {
		t.Fatalf("update must refuse over a symlinked lib, got %v", err)
	}
	if err := cli.uninstall(context.Background(), exe, project, yes("force", "true")); err == nil || !strings.Contains(err.Error(), "install.json") {
		t.Fatalf("uninstall must refuse over a symlinked lib, got %v", err)
	}
	if got := snapshot(t, prefix); got != before["prefix"] {
		t.Fatalf("the prefix changed:\n%s", got)
	}
	if got := snapshot(t, shared); got != before["shared"] {
		t.Fatalf("the shared directory changed:\n%s", got)
	}
	for _, path := range []string{filepath.Join(shared, "bin"), filepath.Join(shared, "share")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist: %v", path, err)
		}
	}
	if target, _ := os.Readlink(filepath.Join(prefix, "bin", "arena")); target != viaPrefix {
		t.Fatalf("bin/arena moved to %q", target)
	}
}

func TestVerifiedInstallAcceptsAPrefixSpelledThroughASymlink(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	alias := filepath.Join(resolvedTemp(t), "alias")
	if err := os.Symlink(e.prefix, alias); err != nil {
		t.Fatal(err)
	}
	e.writeRecord(t, validRecord(alias, "0.1.0", filepath.Join(alias, "lib", "arena", "0.1.0", "arena")))
	cli, _, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, Options{"dry-run": {"true"}}); err != nil {
		t.Fatalf("a manifest that spells the prefix through a symlink is still this install: %v", err)
	}
}

func TestUninstallIgnoresManifestEntriesThatAreNotSkillFiles(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	victim := filepath.Join(e.home, "important.txt")
	writeFile(t, victim, "data", 0o644)
	if err := recordUserSkill(victim, []byte("data")); err != nil {
		t.Fatal(err)
	}
	cli, _, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, yes("force", "true")); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "data" {
		t.Fatalf("an arbitrary manifest path must never be deleted: %q %v", b, err)
	}
}

// A manifest entry shaped like a skill file but outside the user-level
// locations must never be deleted: not by uninstall, not with --force, not
// when its hash matches.
func TestUninstallNeverRemovesSkillShapedEntriesOutsideTheUserLocations(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			setVersion(t, "0.1.0")
			e := newManagedEnv(t, "0.1.0")
			inProject := filepath.Join(e.project, ".arena", "arena-orchestrator", "SKILL.md")
			unknown := filepath.Join(e.home, "elsewhere", "arena-orchestrator", "SKILL.md")
			traversal := e.home + "/.claude/skills/../../elsewhere/arena-orchestrator/SKILL.md"
			for _, p := range []string{inProject, unknown} {
				writeFile(t, p, "content", 0o644)
			}
			m := userSkillManifest{SchemaVersion: 1, Skills: []userSkillEntry{
				{Path: inProject, SHA256: Hash([]byte("content"))},
				{Path: unknown, SHA256: "does-not-match"},
				{Path: traversal, SHA256: Hash([]byte("content"))},
				{Path: "arena-orchestrator/SKILL.md", SHA256: Hash([]byte("content"))},
			}}
			if err := saveUserSkills(m); err != nil {
				t.Fatal(err)
			}
			if entries, ignored, err := trackedSkillCopies(); err != nil || len(entries) != 0 || len(ignored) != 4 {
				t.Fatalf("trackedSkillCopies = %v, %v, %v", entries, ignored, err)
			}

			o := yes()
			if force {
				o = yes("force", "true")
			}
			cli, out, _ := testCLI()
			if err := cli.uninstall(context.Background(), e.exe, e.project, o); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			for _, p := range []string{inProject, unknown} {
				if b, err := os.ReadFile(p); err != nil || string(b) != "content" {
					t.Fatalf("%s must survive uninstall: %q %v", p, b, err)
				}
			}
			if !strings.Contains(out.String(), "ignored user skill manifest entry") {
				t.Fatalf("ignored entries must be reported:\n%s", out.String())
			}
			if _, err := os.Lstat(e.link()); !os.IsNotExist(err) {
				t.Fatalf("the install itself should be gone: %v", err)
			}
		})
	}
}

func TestUninstallRemovesEveryUserSkillCopyArenaInstalls(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	p := &Project{Root: e.project}
	want := map[string]bool{}
	for agent := range skillPaths {
		path, err := p.InstallSkill(agent, true, false)
		if err != nil {
			t.Fatalf("install %s: %v", agent, err)
		}
		want[path] = true
	}
	for _, rel := range []string{".copilot/skills/arena-orchestrator/SKILL.md", ".config/opencode/skills/arena-orchestrator/SKILL.md"} {
		if !want[filepath.Join(e.home, rel)] {
			t.Fatalf("user install did not write %s: %v", rel, want)
		}
	}
	if entries, ignored, err := trackedSkillCopies(); err != nil || len(entries) != len(want) || len(ignored) != 0 {
		t.Fatalf("every installed copy must be tracked: %d of %d, ignored %v, %v", len(entries), len(want), ignored, err)
	}
	cli, _, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, yes()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for path := range want {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s should be removed: %v", path, err)
		}
	}
}

func TestUninstallKeepsSkillCopiesReachedThroughSymlinks(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	// ~/.claude is a symlink to a directory elsewhere.
	real := filepath.Join(e.home, "dotfiles", "claude")
	writeFile(t, filepath.Join(real, "skills", "arena-orchestrator", "SKILL.md"), "installed", 0o644)
	if err := os.Symlink(real, filepath.Join(e.home, ".claude")); err != nil {
		t.Fatal(err)
	}
	// ~/.agents/.../SKILL.md is itself a symlink to a file elsewhere.
	target := filepath.Join(e.home, "notes.md")
	writeFile(t, target, "installed", 0o644)
	linked := filepath.Join(e.home, ".agents", "skills", "arena-orchestrator", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	// ~/.gemini/.../SKILL.md is a directory.
	dirCopy := filepath.Join(e.home, ".gemini", "skills", "arena-orchestrator", "SKILL.md")
	if err := os.MkdirAll(dirCopy, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md"), linked, dirCopy} {
		if err := recordUserSkill(p, []byte("installed")); err != nil {
			t.Fatal(err)
		}
	}
	cli, out, _ := testCLI()
	if err := cli.uninstall(context.Background(), e.exe, e.project, yes("force", "true")); err != nil {
		t.Fatalf("uninstall --force: %v", err)
	}
	for _, p := range []string{filepath.Join(real, "skills", "arena-orchestrator", "SKILL.md"), target} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "installed" {
			t.Fatalf("%s must be untouched: %q %v", p, b, err)
		}
	}
	if info, err := os.Lstat(linked); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the symlinked copy must stay: %v", err)
	}
	if info, err := os.Stat(dirCopy); err != nil || !info.IsDir() {
		t.Fatalf("the directory must stay: %v", err)
	}
	if !strings.Contains(out.String(), "is a symlink") || !strings.Contains(out.String(), "not a regular file") {
		t.Fatalf("the checklist must say why the copies were kept:\n%s", out.String())
	}
	if backups, _ := filepath.Glob(filepath.Join(e.home, "state", "arena", "skill-backups", "*")); len(backups) != 0 {
		t.Fatalf("nothing may be backed up for a refused path: %v", backups)
	}
}

// removeSkillCopy re-checks right before deleting, so a path that was fine when
// the plan was made but was swapped for a symlink since is left alone.
func TestRemoveSkillCopyRechecksTheFileBeforeDeleting(t *testing.T) {
	home := resolvedTemp(t)
	dir := filepath.Join(home, ".claude", "skills", "arena-orchestrator")
	path := filepath.Join(dir, "SKILL.md")
	writeFile(t, path, "installed", 0o644)

	elsewhere := filepath.Join(resolvedTemp(t), "arena-orchestrator")
	writeFile(t, filepath.Join(elsewhere, "SKILL.md"), "installed", 0o644)
	if err := os.Rename(dir, dir+".bak"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}
	if err := removeSkillCopy(home, path, Hash([]byte("installed")), ""); err == nil {
		t.Fatal("a path that now goes through a symlink must not be removed")
	}
	if err := removeSkillCopy(home, path, "", filepath.Join(home, "backup.md")); err == nil {
		t.Fatal("the --force path must re-check too")
	}
	if b, err := os.ReadFile(filepath.Join(elsewhere, "SKILL.md")); err != nil || string(b) != "installed" {
		t.Fatalf("the symlink target must survive: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(home, "backup.md")); !os.IsNotExist(err) {
		t.Fatalf("nothing may be backed up for a refused path: %v", err)
	}

	// Edited after the plan: the recorded hash no longer matches.
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir+".bak", dir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "edited since", 0o644)
	if err := removeSkillCopy(home, path, Hash([]byte("installed")), ""); err == nil {
		t.Fatal("a copy edited since the plan must be left alone")
	}
	if err := removeSkillCopy(home, path, Hash([]byte("edited since")), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the emptied skill directory should go too: %v", err)
	}
}

func TestUpdateNoticeShowsAtMostOncePerInterval(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "state", "update-check.json")
	calls := 0
	latest := "0.2.0"
	lookup := func(context.Context) (string, error) { calls++; return latest, nil }
	start := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

	line := updateNotice(context.Background(), cachePath, "0.1.0", start, lookup)
	if !strings.Contains(line, "arena 0.2.0 is out") || !strings.Contains(line, "you're on 0.1.0") || !strings.Contains(line, "arena update") {
		t.Fatalf("notice = %q", line)
	}
	var cache updateCheckCache
	if err := ReadJSON(cachePath, &cache); err != nil || cache.Latest != "0.2.0" || cache.CheckedAt != "2026-10-17T09:00:00Z" {
		t.Fatalf("cache = %+v %v", cache, err)
	}
	for _, later := range []time.Duration{time.Minute, 12 * time.Hour, 24*time.Hour - time.Second} {
		if line := updateNotice(context.Background(), cachePath, "0.1.0", start.Add(later), lookup); line != "" || calls != 1 {
			t.Fatalf("within 24h (+%s): notice %q, lookups %d; want silence and no lookup", later, line, calls)
		}
	}
	if line := updateNotice(context.Background(), cachePath, "0.1.0", start.Add(24*time.Hour), lookup); line == "" || calls != 2 {
		t.Fatalf("after 24h: notice %q, lookups %d", line, calls)
	}

	// Up to date, or ahead of the release: look, cache, stay quiet.
	at := start.Add(48 * time.Hour)
	if line := updateNotice(context.Background(), cachePath, "0.2.0", at, lookup); line != "" {
		t.Fatalf("up to date must be silent: %q", line)
	}
	at = at.Add(25 * time.Hour)
	if line := updateNotice(context.Background(), cachePath, "0.3.0-dev", at, lookup); line != "" {
		t.Fatalf("ahead of the release must be silent: %q", line)
	}

	// A failed lookup counts as a check, keeps the last known release and prints nothing.
	at = at.Add(25 * time.Hour)
	failing := func(context.Context) (string, error) { calls++; return "", errors.New("offline") }
	before := calls
	if line := updateNotice(context.Background(), cachePath, "0.1.0", at, failing); line != "" || calls != before+1 {
		t.Fatalf("failed lookup: notice %q, lookups %d", line, calls-before)
	}
	if err := ReadJSON(cachePath, &cache); err != nil || cache.Latest != "0.2.0" {
		t.Fatalf("a failed lookup must keep the last known release: %+v %v", cache, err)
	}
	if line := updateNotice(context.Background(), cachePath, "0.1.0", at.Add(time.Hour), failing); line != "" || calls != before+1 {
		t.Fatalf("an offline machine must not retry on every command: notice %q, lookups %d", line, calls-before)
	}
}

func TestUpdateNoticeRefreshesWhenTheClockMovedBack(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	writeFile(t, cachePath, `{"checkedAt":"2030-01-01T00:00:00Z","latest":"0.1.0"}`, 0o644)
	calls := 0
	lookup := func(context.Context) (string, error) { calls++; return "0.2.0", nil }
	if line := updateNotice(context.Background(), cachePath, "0.1.0", time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), lookup); line == "" || calls != 1 {
		t.Fatalf("a cache from the future must not suppress the check: %q %d", line, calls)
	}
}

func TestUpdateNoticeNeverWaitsPastTheTimeout(t *testing.T) {
	old := updateCheckTimeout
	updateCheckTimeout = 50 * time.Millisecond
	t.Cleanup(func() { updateCheckTimeout = old })
	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	slow := func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "9.9.9", nil
		}
	}
	started := time.Now()
	if line := updateNotice(context.Background(), cachePath, "0.1.0", time.Now(), slow); line != "" {
		t.Fatalf("a timed-out lookup must print nothing: %q", line)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the check waited %s", elapsed)
	}

	// The real lookup honors the same deadline against a hanging server.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer hang.Close()
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	started = time.Now()
	if _, err := latestRelease(ctx, hang.URL); err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("latestRelease must stop at the deadline: %v after %s", err, time.Since(started))
	}
}

func TestUpdateNoticeEligible(t *testing.T) {
	env := func(pairs ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(pairs); i += 2 {
				if pairs[i] == k {
					return pairs[i+1]
				}
			}
			return ""
		}
	}
	for _, c := range []struct {
		name string
		pos  []string
		o    Options
		env  func(string) string
		want bool
	}{
		{"interactive command", []string{"status"}, Options{}, env(), true},
		{"bare arena", nil, Options{}, env(), true},
		{"json output", []string{"status"}, Options{"json": {"true"}}, env(), false},
		{"opt out", []string{"status"}, Options{}, env("ARENA_NO_UPDATE_CHECK", "1"), false},
		{"opt out set to 0", []string{"status"}, Options{}, env("ARENA_NO_UPDATE_CHECK", "0"), true},
		{"mcp", []string{"mcp"}, Options{}, env(), false},
		{"rpc", []string{"rpc"}, Options{}, env(), false},
		{"bridge", []string{"bridge"}, Options{}, env(), false},
		{"update", []string{"update"}, Options{}, env(), false},
		{"uninstall", []string{"uninstall"}, Options{}, env(), false},
	} {
		if got := updateNoticeEligible(c.pos, c.o, c.env); got != c.want {
			t.Errorf("%s: eligible = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRunMakesNoReleaseRequestOffATerminal(t *testing.T) {
	s := newReleaseServer(t, "9.9.9", nil)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cli, out, errOut := testCLI()
	if err := cli.Run(context.Background(), []string{"version"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "arena") || errOut.Len() != 0 {
		t.Fatalf("out %q err %q", out.String(), errOut.String())
	}
	if n := s.hitCount("/latest"); n != 0 {
		t.Fatalf("a non-terminal stderr must not trigger a release lookup, got %d", n)
	}
}

// A tracked copy that is a symlink, or sits behind one, is not something
// `arena skill install --replace` will write (SafePath refuses it), so the
// update must not suggest refreshing it.
func TestUpdateDoesNotListSymlinkedSkillCopiesAsStale(t *testing.T) {
	setVersion(t, "0.1.0")
	e := newManagedEnv(t, "0.1.0")
	plain := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
	writeFile(t, plain, "old skill", 0o644)

	// ~/.gemini/.../SKILL.md is itself a symlink to a file elsewhere.
	fileTarget := filepath.Join(e.home, "notes.md")
	writeFile(t, fileTarget, "old skill", 0o644)
	fileLink := filepath.Join(e.home, ".gemini", "skills", "arena-orchestrator", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(fileLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fileTarget, fileLink); err != nil {
		t.Fatal(err)
	}

	// ~/.agents is a symlink to a directory elsewhere.
	realAgents := filepath.Join(e.home, "dotfiles", "agents")
	writeFile(t, filepath.Join(realAgents, "skills", "arena-orchestrator", "SKILL.md"), "old skill", 0o644)
	if err := os.Symlink(realAgents, filepath.Join(e.home, ".agents")); err != nil {
		t.Fatal(err)
	}
	viaDir := filepath.Join(e.home, ".agents", "skills", "arena-orchestrator", "SKILL.md")

	for _, p := range []string{plain, fileLink, viaDir} {
		if err := recordUserSkill(p, []byte("old skill")); err != nil {
			t.Fatal(err)
		}
	}
	if stale := staleSkillCopies([]byte("new skill")); len(stale) != 1 || stale[0] != plain {
		t.Fatalf("staleSkillCopies = %v, want only %s", stale, plain)
	}

	newReleaseServer(t, "0.2.0", releaseFiles(t, "0.2.0", fakeBinary("0.2.0"), "new skill"))
	cli, out, _ := testCLI()
	if err := cli.update(context.Background(), e.exe, e.project, yes()); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out.String(), plain) {
		t.Fatalf("the plain stale copy must be listed:\n%s", out.String())
	}
	for _, p := range []string{fileLink, viaDir} {
		if strings.Contains(out.String(), p) {
			t.Fatalf("%s is reached through a symlink and must not be listed:\n%s", p, out.String())
		}
	}
}

// A reused version's skill is not compared, and the update says so once: not
// at all when every tracked copy is gone, since there is nothing to refresh.
func TestUpdateReuseAdviceIsOneLineAndNeedsAnExistingCopy(t *testing.T) {
	reuse := func(t *testing.T, copyExists bool) string {
		setVersion(t, "0.2.0")
		e := newManagedEnv(t, "0.2.0")
		writeFile(t, filepath.Join(e.prefix, "lib", "arena", "0.1.0", "arena"), fakeBinary("0.1.0"), 0o755)
		newReleaseServer(t, "0.2.0", nil)
		copyPath := filepath.Join(e.home, ".claude", "skills", "arena-orchestrator", "SKILL.md")
		writeFile(t, copyPath, "older skill", 0o644)
		if err := recordUserSkill(copyPath, []byte("older skill")); err != nil {
			t.Fatal(err)
		}
		if !copyExists {
			if err := os.Remove(copyPath); err != nil {
				t.Fatal(err)
			}
		}
		cli, out, _ := testCLI()
		if err := cli.update(context.Background(), e.exe, e.project, yes("to", "0.1.0")); err != nil {
			t.Fatalf("update --to: %v", err)
		}
		return out.String()
	}
	t.Run("a tracked copy exists", func(t *testing.T) {
		out := reuse(t, true)
		if n := strings.Count(out, "was not compared"); n != 1 {
			t.Fatalf("want exactly one line saying the skill was not compared, got %d:\n%s", n, out)
		}
		if n := strings.Count(out, "arena skill install"); n > 1 {
			t.Fatalf("the refresh command is printed %d times:\n%s", n, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "skill install") && !strings.Contains(line, "was not compared") {
				t.Fatalf("the advice must be a single line, found a second one %q in:\n%s", line, out)
			}
		}
	})
	t.Run("every tracked copy is gone", func(t *testing.T) {
		if out := reuse(t, false); strings.Contains(out, "not compared") || strings.Contains(out, "skill install") {
			t.Fatalf("no advice is due when no tracked copy exists:\n%s", out)
		}
	})
}

// When the link cannot be switched, a version this run downloaded leaves the
// store again, but a version that was already there is never removed.
func TestUpdateSwitchFailureRemovesOnlyWhatThisRunPlaced(t *testing.T) {
	// A binary whose `version` answer is right but which first swaps bin/arena
	// for a regular file, so the smoke test passes and switchLink refuses.
	saboteur := func(link, version string) string {
		return "#!/bin/sh\nrm -f '" + link + "'\necho regular > '" + link + "'\necho \"arena " + version + "\"\n"
	}
	t.Run("downloaded", func(t *testing.T) {
		setVersion(t, "0.1.0")
		e := newManagedEnv(t, "0.1.0")
		manifest := filepath.Join(e.prefix, "share", "arena", "install.json")
		before, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		newReleaseServer(t, "0.2.0", releaseFiles(t, "0.2.0", saboteur(e.link(), "0.2.0"), "skill"))
		cli, _, _ := testCLI()
		err = cli.update(context.Background(), e.exe, e.project, yes())
		if err == nil || !strings.Contains(err.Error(), "is not a symlink") {
			t.Fatalf("update must fail when the link cannot be switched, got %v", err)
		}
		if _, err := os.Lstat(filepath.Join(e.prefix, "lib", "arena", "0.2.0")); !os.IsNotExist(err) {
			t.Fatalf("the version this run placed must be removed: %v", err)
		}
		if _, err := os.Stat(e.exe); err != nil {
			t.Fatalf("the running version must stay in the store: %v", err)
		}
		if after, _ := os.ReadFile(manifest); !bytes.Equal(after, before) {
			t.Fatalf("install.json must not change:\n%s", after)
		}
	})
	t.Run("reused", func(t *testing.T) {
		setVersion(t, "0.2.0")
		e := newManagedEnv(t, "0.2.0")
		old := filepath.Join(e.prefix, "lib", "arena", "0.1.0", "arena")
		script := saboteur(e.link(), "0.1.0")
		writeFile(t, old, script, 0o755)
		newReleaseServer(t, "0.2.0", nil)
		cli, _, _ := testCLI()
		err := cli.update(context.Background(), e.exe, e.project, yes("to", "0.1.0"))
		if err == nil || !strings.Contains(err.Error(), "is not a symlink") {
			t.Fatalf("update must fail when the link cannot be switched, got %v", err)
		}
		if b, err := os.ReadFile(old); err != nil || string(b) != script {
			t.Fatalf("a version that was already in the store must be left alone: %q %v", b, err)
		}
	})
}
