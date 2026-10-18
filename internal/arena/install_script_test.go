package arena

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const installTestVersion = "0.1.0-test"

func installScriptPath(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		path := filepath.Join(dir, "scripts", "install.sh")
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("scripts/install.sh not found above the test directory")
		}
		dir = parent
	}
}

func requireCurl(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is not available")
	}
}

func fakeArenaBinary(version string) []byte {
	return []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo \"arena " + version + "\"; fi\n")
}

func writeFakeArena(t *testing.T, path, version string) []byte {
	t.Helper()
	b := fakeArenaBinary(version)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return b
}

// runInstallScript runs install.sh with a throwaway HOME and TMPDIR and returns
// the combined output and the TMPDIR it used, so callers can check for leftovers.
func runInstallScript(t *testing.T, prefix string, env []string, args ...string) (string, string, error) {
	t.Helper()
	script := installScriptPath(t)
	tmp := t.TempDir()
	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"TMPDIR="+tmp,
		"ARENA_PREFIX="+prefix,
		"ARENA_VERSION=",
		"ARENA_RELEASE_BASE_URL=",
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), tmp, err
}

type installManifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Prefix        string `json:"prefix"`
	Version       string `json:"version"`
	StorePath     string `json:"storePath"`
	BinarySha256  string `json:"binarySha256"`
	Source        string `json:"source"`
	InstalledAt   string `json:"installedAt"`
}

// assertInstalled checks an install under prefix. The script records physical
// paths, so every expectation is built from the resolved prefix: t.TempDir can
// sit behind a symlink (/var is /private/var on macOS).
func assertInstalled(t *testing.T, prefix, version, source string, binary []byte) {
	t.Helper()
	physical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(physical, "lib", "arena", version, "arena")
	info, err := os.Stat(store)
	if err != nil {
		t.Fatalf("store binary missing: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("store binary mode = %v, want 0755", info.Mode().Perm())
	}
	link := filepath.Join(physical, "bin", "arena")
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("bin/arena is not a symlink: %v, %v", fi, err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != store {
		t.Fatalf("bin/arena points at %q, want the physical store binary %q", target, store)
	}
	raw, err := os.ReadFile(filepath.Join(physical, "share", "arena", "install.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m installManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("install.json is not valid JSON: %v\n%s", err, raw)
	}
	if m.SchemaVersion != 1 || m.Prefix != physical || m.Version != version || m.StorePath != store || m.Source != source {
		t.Fatalf("unexpected manifest: %+v (want prefix %q, storePath %q)", m, physical, store)
	}
	if resolved, err := filepath.EvalSymlinks(m.StorePath); err != nil || resolved != m.StorePath {
		t.Fatalf("storePath %q is not physical (resolves to %q, %v)", m.StorePath, resolved, err)
	}
	if m.BinarySha256 != sha256Hex(binary) {
		t.Fatalf("binarySha256 = %s, want %s", m.BinarySha256, sha256Hex(binary))
	}
	if _, err := time.Parse(time.RFC3339, m.InstalledAt); err != nil {
		t.Fatalf("installedAt %q is not RFC 3339: %v", m.InstalledAt, err)
	}
	// Temp files from the atomic writes must not be left behind.
	for _, dir := range []string{filepath.Join(physical, "bin"), filepath.Join(physical, "share", "arena"), filepath.Dir(store)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				t.Fatalf("leftover temp file %s", filepath.Join(dir, e.Name()))
			}
		}
	}
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s should be empty, found %d entries (first: %s)", dir, len(entries), entries[0].Name())
	}
}

func TestInstallScriptFromLocalBinary(t *testing.T) {
	prefix := t.TempDir()
	src := filepath.Join(t.TempDir(), "arena")
	binary := writeFakeArena(t, src, installTestVersion)
	for run := 1; run <= 2; run++ {
		out, _, err := runInstallScript(t, prefix, nil, "--from", src)
		if err != nil {
			t.Fatalf("run %d failed: %v\n%s", run, err, out)
		}
		if !strings.Contains(out, "arena "+installTestVersion) {
			t.Fatalf("run %d did not print the installed version:\n%s", run, out)
		}
		assertInstalled(t, prefix, installTestVersion, "make", binary)
	}
}

func TestInstallScriptWarnsWhenBinDirIsNotOnPath(t *testing.T) {
	prefix := t.TempDir()
	src := filepath.Join(t.TempDir(), "arena")
	writeFakeArena(t, src, installTestVersion)
	out, _, err := runInstallScript(t, prefix, []string{"PATH=/usr/bin:/bin"}, "--from", src)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	want := fmt.Sprintf(`export PATH="%s:$PATH"`, filepath.Join(prefix, "bin"))
	if !strings.Contains(out, want) {
		t.Fatalf("output is missing %q:\n%s", want, out)
	}
	out, _, err = runInstallScript(t, prefix, []string{"PATH=" + filepath.Join(prefix, "bin") + ":/usr/bin:/bin"}, "--from", src)
	if err != nil {
		t.Fatalf("second install failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "export PATH") {
		t.Fatalf("PATH hint printed although bin is on PATH:\n%s", out)
	}
}

// A prefix reached through a symlink is recorded by its physical path, and the
// script creates a prefix that does not exist yet.
func TestInstallScriptRecordsPhysicalPrefix(t *testing.T) {
	real := t.TempDir()
	linked := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(real, linked); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	src := filepath.Join(t.TempDir(), "arena")
	binary := writeFakeArena(t, src, installTestVersion)
	for _, tc := range []struct{ name, prefix string }{
		{"symlinked", linked},
		{"not created", filepath.Join(linked, "new", "prefix")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := runInstallScript(t, tc.prefix, nil, "--from", src)
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			assertInstalled(t, tc.prefix, installTestVersion, "make", binary)
		})
	}
}

func TestInstallScriptRefusesStoreBehindSymlink(t *testing.T) {
	prefix := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(prefix, "lib", "arena")); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	src := filepath.Join(t.TempDir(), "arena")
	writeFakeArena(t, src, installTestVersion)
	out, _, err := runInstallScript(t, prefix, nil, "--from", src)
	if err == nil {
		t.Fatalf("expected failure, got:\n%s", out)
	}
	if !strings.Contains(out, "symbolic link") {
		t.Fatalf("expected a symlink error, got:\n%s", out)
	}
	assertEmptyDir(t, elsewhere)
	if _, statErr := os.Lstat(filepath.Join(prefix, "bin", "arena")); statErr == nil {
		t.Fatal("link created although the store is behind a symlink")
	}
}

// Every layout refusal happens before the download and before any directory is
// made, so a refused install leaves the prefix exactly as it found it.
func TestInstallScriptRefusalCreatesNothingUnderThePrefix(t *testing.T) {
	scenarios := []struct {
		name, want string
		setup      func(t *testing.T, prefix, elsewhere string)
	}{
		{"lib is a symlink", "symbolic link", func(t *testing.T, prefix, elsewhere string) {
			if err := os.Symlink(elsewhere, filepath.Join(prefix, "lib")); err != nil {
				t.Skipf("cannot create a symlink: %v", err)
			}
		}},
		{"lib/arena is a symlink", "symbolic link", func(t *testing.T, prefix, elsewhere string) {
			if err := os.MkdirAll(filepath.Join(prefix, "lib"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(prefix, "lib", "arena")); err != nil {
				t.Skipf("cannot create a symlink: %v", err)
			}
		}},
		{"lib/arena is a dangling symlink", "symbolic link", func(t *testing.T, prefix, elsewhere string) {
			if err := os.MkdirAll(filepath.Join(prefix, "lib"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(elsewhere, "missing"), filepath.Join(prefix, "lib", "arena")); err != nil {
				t.Skipf("cannot create a symlink: %v", err)
			}
		}},
		{"lib is a file", "not a directory", func(t *testing.T, prefix, elsewhere string) {
			if err := os.WriteFile(filepath.Join(prefix, "lib"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"bin/arena is a directory", "is a directory", func(t *testing.T, prefix, elsewhere string) {
			if err := os.MkdirAll(filepath.Join(prefix, "bin", "arena"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name+"/from", func(t *testing.T) {
			prefix, elsewhere := t.TempDir(), t.TempDir()
			sc.setup(t, prefix, elsewhere)
			before := snapshot(t, prefix)
			src := filepath.Join(t.TempDir(), "arena")
			writeFakeArena(t, src, installTestVersion)
			out, _, err := runInstallScript(t, prefix, nil, "--from", src)
			if err == nil {
				t.Fatalf("expected failure, got:\n%s", out)
			}
			if !strings.Contains(out, sc.want) {
				t.Fatalf("expected %q in the error, got:\n%s", sc.want, out)
			}
			if after := snapshot(t, prefix); after != before {
				t.Fatalf("the prefix changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			assertEmptyDir(t, elsewhere)
		})
		t.Run(sc.name+"/release", func(t *testing.T) {
			requireCurl(t)
			platform := hostInstallPlatform(t)
			archive := installTestTarball(t, fakeArenaBinary(installTestVersion))
			srv := newInstallTestServer(t, platform, archive, sha256Hex(archive)+"  {name}\n")
			prefix, elsewhere := t.TempDir(), t.TempDir()
			sc.setup(t, prefix, elsewhere)
			before := snapshot(t, prefix)
			out, tmp, err := runInstallScript(t, prefix, []string{"ARENA_RELEASE_BASE_URL=" + srv.URL})
			if err == nil {
				t.Fatalf("expected failure, got:\n%s", out)
			}
			if !strings.Contains(out, sc.want) {
				t.Fatalf("expected %q in the error, got:\n%s", sc.want, out)
			}
			if hits := srv.requested(); len(hits) != 0 {
				t.Fatalf("a refused layout must be reported before downloading, got requests %v", hits)
			}
			if after := snapshot(t, prefix); after != before {
				t.Fatalf("the prefix changed:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			assertEmptyDir(t, elsewhere)
			assertEmptyDir(t, tmp)
		})
	}
}

func TestInstallScriptMovesPlainFileIntoStore(t *testing.T) {
	prefix := t.TempDir()
	legacy := writeFakeArena(t, filepath.Join(prefix, "bin", "arena"), "0.0.9-old")
	src := filepath.Join(t.TempDir(), "arena")
	binary := writeFakeArena(t, src, installTestVersion)
	if out, _, err := runInstallScript(t, prefix, nil, "--from", src); err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	assertInstalled(t, prefix, installTestVersion, "make", binary)
	kept, err := os.ReadFile(filepath.Join(prefix, "lib", "arena", "0.0.9-old", "arena"))
	if err != nil {
		t.Fatalf("old binary was not kept in the store: %v", err)
	}
	if !bytes.Equal(kept, legacy) {
		t.Fatal("stored copy of the old binary differs from the original")
	}
}

func TestInstallScriptRejectsMissingBinary(t *testing.T) {
	prefix := t.TempDir()
	out, _, err := runInstallScript(t, prefix, nil, "--from", filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatalf("expected failure, got:\n%s", out)
	}
	if _, statErr := os.Lstat(filepath.Join(prefix, "bin", "arena")); statErr == nil {
		t.Fatal("link created for a missing binary")
	}
}

func installTestTarball(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "arena", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type installTestServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits []string
}

func (s *installTestServer) requested() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...)
}

// newInstallTestServer serves a release the way GitHub does: /latest redirects
// to the tag page, and only the archive for platform ("linux_amd64") exists.
func newInstallTestServer(t *testing.T, platform string, archive []byte, sums string) *installTestServer {
	t.Helper()
	s := &installTestServer{}
	name := "arena_" + installTestVersion + "_" + platform + ".tar.gz"
	dir := "/download/v" + installTestVersion + "/"
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.URL.Path)
		s.mu.Unlock()
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "http://"+r.Host+"/tag/v"+installTestVersion, http.StatusFound)
		case dir + name:
			_, _ = w.Write(archive)
		case dir + "SHA256SUMS":
			_, _ = w.Write([]byte(strings.ReplaceAll(sums, "{name}", name)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func hostInstallPlatform(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64":
		return runtime.GOOS + "_" + runtime.GOARCH
	}
	t.Skipf("no release archive for %s/%s", runtime.GOOS, runtime.GOARCH)
	return ""
}

func TestInstallScriptReleaseDownload(t *testing.T) {
	requireCurl(t)
	platform := hostInstallPlatform(t)
	binary := fakeArenaBinary(installTestVersion)
	archive := installTestTarball(t, binary)
	sums := sha256Hex(archive) + "  {name}\n" + strings.Repeat("0", 64) + "  arena_" + installTestVersion + "_other.tar.gz\n"
	for _, tc := range []struct {
		name string
		env  string
		want []string
	}{
		{"latest", "", []string{"/latest", "/download/v" + installTestVersion + "/arena_" + installTestVersion + "_" + platform + ".tar.gz", "/download/v" + installTestVersion + "/SHA256SUMS"}},
		{"pinned", "ARENA_VERSION=v" + installTestVersion, []string{"/download/v" + installTestVersion + "/arena_" + installTestVersion + "_" + platform + ".tar.gz", "/download/v" + installTestVersion + "/SHA256SUMS"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newInstallTestServer(t, platform, archive, sums)
			prefix := t.TempDir()
			env := []string{"ARENA_RELEASE_BASE_URL=" + srv.URL}
			if tc.env != "" {
				env = append(env, tc.env)
			}
			out, tmp, err := runInstallScript(t, prefix, env)
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			assertInstalled(t, prefix, installTestVersion, "release", binary)
			assertEmptyDir(t, tmp)
			got := srv.requested()
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("requests = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstallScriptRejectsChecksumMismatch(t *testing.T) {
	requireCurl(t)
	platform := hostInstallPlatform(t)
	archive := installTestTarball(t, fakeArenaBinary(installTestVersion))
	srv := newInstallTestServer(t, platform, archive, strings.Repeat("0", 64)+"  {name}\n")
	prefix := t.TempDir()
	out, tmp, err := runInstallScript(t, prefix, []string{"ARENA_RELEASE_BASE_URL=" + srv.URL})
	if err == nil {
		t.Fatalf("install succeeded with a wrong checksum:\n%s", out)
	}
	if !strings.Contains(out, "checksum mismatch") {
		t.Fatalf("expected a checksum error, got:\n%s", out)
	}
	if _, statErr := os.Lstat(filepath.Join(prefix, "bin", "arena")); statErr == nil {
		t.Fatal("link created despite the checksum failure")
	}
	assertEmptyDir(t, prefix)
	assertEmptyDir(t, tmp)
}

// Shim uname so the platform mapping is checked on any host.
func TestInstallScriptPlatforms(t *testing.T) {
	requireCurl(t)
	installScriptPath(t)
	shim := t.TempDir()
	shimScript := "#!/bin/sh\ncase \"$1\" in -s) echo \"$FAKE_UNAME_S\" ;; -m) echo \"$FAKE_UNAME_M\" ;; *) echo \"$FAKE_UNAME_S\" ;; esac\n"
	if err := os.WriteFile(filepath.Join(shim, "uname"), []byte(shimScript), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := fakeArenaBinary(installTestVersion)
	archive := installTestTarball(t, binary)
	sums := sha256Hex(archive) + "  {name}\n"
	for _, tc := range []struct {
		os, arch, platform string
	}{
		{"Linux", "x86_64", "linux_amd64"},
		{"Linux", "aarch64", "linux_arm64"},
		{"Darwin", "arm64", "darwin_arm64"},
		{"Darwin", "x86_64", "darwin_amd64"},
		{"MINGW64_NT-10.0", "x86_64", ""},
		{"MSYS_NT-10.0", "x86_64", ""},
		{"CYGWIN_NT-10.0", "x86_64", ""},
		{"FreeBSD", "amd64", ""},
		{"Linux", "riscv64", ""},
		{"Linux", "i686", ""},
	} {
		t.Run(tc.os+"/"+tc.arch, func(t *testing.T) {
			platform := tc.platform
			if platform == "" {
				platform = "linux_amd64"
			}
			srv := newInstallTestServer(t, platform, archive, sums)
			prefix := t.TempDir()
			env := []string{
				"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH"),
				"FAKE_UNAME_S=" + tc.os,
				"FAKE_UNAME_M=" + tc.arch,
				"ARENA_RELEASE_BASE_URL=" + srv.URL,
			}
			out, _, err := runInstallScript(t, prefix, env)
			if tc.platform == "" {
				if err == nil {
					t.Fatalf("expected %s/%s to be refused:\n%s", tc.os, tc.arch, out)
				}
				if hits := srv.requested(); len(hits) != 0 {
					t.Fatalf("refused platform still made requests: %v", hits)
				}
				assertEmptyDir(t, prefix)
				return
			}
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			assertInstalled(t, prefix, installTestVersion, "release", binary)
		})
	}
}

// The version becomes a directory name and part of a URL, so it must match
// MAJOR.MINOR.PATCH[-prerelease] before any path or request is built from it.
func TestInstallScriptRejectsInvalidPinnedVersions(t *testing.T) {
	requireCurl(t)
	platform := hostInstallPlatform(t)
	archive := installTestTarball(t, fakeArenaBinary(installTestVersion))
	sums := sha256Hex(archive) + "  {name}\n"
	for _, v := range []string{
		"v0.2", "0.2", "../x", "1.2.3/../x", "1.2.3+build", "1.2.3-", "1.2", "vv1.2.3",
		"1.2.3\n../x", " 1.2.3", "1.2.3 ", "1.2.3-rc/../../x", "a.b.c", "1.2.3_4",
	} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			srv := newInstallTestServer(t, platform, archive, sums)
			prefix := t.TempDir()
			out, tmp, err := runInstallScript(t, prefix, []string{"ARENA_RELEASE_BASE_URL=" + srv.URL, "ARENA_VERSION=" + v})
			if err == nil {
				t.Fatalf("ARENA_VERSION=%q was accepted:\n%s", v, out)
			}
			if !strings.Contains(out, "not a release version") || !strings.Contains(out, "MAJOR.MINOR.PATCH") {
				t.Fatalf("expected a version error, got:\n%s", out)
			}
			if hits := srv.requested(); len(hits) != 0 {
				t.Fatalf("an invalid version still made requests: %v", hits)
			}
			assertEmptyDir(t, prefix)
			assertEmptyDir(t, tmp)
		})
	}
}

func TestInstallScriptRejectsInvalidLatestTag(t *testing.T) {
	requireCurl(t)
	hostInstallPlatform(t)
	for _, tag := range []string{"v0.2", "0.2", "latest", "v1.2.3+build"} {
		t.Run(tag, func(t *testing.T) {
			var mu sync.Mutex
			var hits []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hits = append(hits, r.URL.Path)
				mu.Unlock()
				if r.URL.Path == "/latest" {
					http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
					return
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()
			prefix := t.TempDir()
			out, tmp, err := runInstallScript(t, prefix, []string{"ARENA_RELEASE_BASE_URL=" + srv.URL})
			if err == nil {
				t.Fatalf("latest tag %q was accepted:\n%s", tag, out)
			}
			if !strings.Contains(out, "not a release version") {
				t.Fatalf("expected a version error, got:\n%s", out)
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.Join(hits, ",") != "/latest" {
				t.Fatalf("requests = %v, want only /latest", hits)
			}
			assertEmptyDir(t, prefix)
			assertEmptyDir(t, tmp)
		})
	}
}

func TestInstallScriptRejectsInvalidBinaryVersion(t *testing.T) {
	for _, v := range []string{"v0.2.0", "0.2", "../x", "1.2.3+build"} {
		t.Run(v, func(t *testing.T) {
			prefix := t.TempDir()
			src := filepath.Join(t.TempDir(), "arena")
			writeFakeArena(t, src, v)
			out, _, err := runInstallScript(t, prefix, nil, "--from", src)
			if err == nil {
				t.Fatalf("binary version %q was accepted:\n%s", v, out)
			}
			if !strings.Contains(out, "not a release version") {
				t.Fatalf("expected a version error, got:\n%s", out)
			}
			assertEmptyDir(t, prefix)
		})
	}
}

// A binary that reports another version than its release tag is a bad release,
// so the install stops with nothing linked, nothing stored and no temp files.
func TestInstallScriptRejectsVersionMismatch(t *testing.T) {
	requireCurl(t)
	platform := hostInstallPlatform(t)
	archive := installTestTarball(t, fakeArenaBinary("0.1.0-other"))
	srv := newInstallTestServer(t, platform, archive, sha256Hex(archive)+"  {name}\n")
	prefix := t.TempDir()
	out, tmp, err := runInstallScript(t, prefix, []string{"ARENA_RELEASE_BASE_URL=" + srv.URL})
	if err == nil {
		t.Fatalf("install succeeded although the binary reports another version:\n%s", out)
	}
	if !strings.Contains(out, "reports version '0.1.0-other', not "+installTestVersion) {
		t.Fatalf("expected a version mismatch error, got:\n%s", out)
	}
	if _, statErr := os.Lstat(filepath.Join(prefix, "bin", "arena")); statErr == nil {
		t.Fatal("link created despite the version mismatch")
	}
	assertEmptyDir(t, prefix)
	assertEmptyDir(t, tmp)
}

// Asking the installed binary for its version must never start the update
// notice, which would reach the network from the installer.
func TestInstallScriptDisablesUpdateCheckWhenReadingVersion(t *testing.T) {
	strict := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then\n" +
		"  if [ \"${ARENA_NO_UPDATE_CHECK:-}\" != 1 ]; then echo 'update notice' >&2; exit 3; fi\n" +
		"  echo 'arena " + installTestVersion + "'\nfi\n")
	src := filepath.Join(t.TempDir(), "arena")
	if err := os.WriteFile(src, strict, 0o755); err != nil {
		t.Fatal(err)
	}
	prefix := t.TempDir()
	// Install twice: the second run also reads the version of the stored binary.
	for run := 1; run <= 2; run++ {
		out, _, err := runInstallScript(t, prefix, []string{"ARENA_NO_UPDATE_CHECK=0"}, "--from", src)
		if err != nil {
			t.Fatalf("run %d failed: %v\n%s", run, err, out)
		}
		if !strings.Contains(out, "arena "+installTestVersion) {
			t.Fatalf("run %d did not print the installed version:\n%s", run, out)
		}
	}
	assertInstalled(t, prefix, installTestVersion, "make", strict)
}
