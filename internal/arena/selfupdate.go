package arena

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
)

// Releases are GitHub releases. A managed install keeps each version at
// <prefix>/lib/arena/<version>/arena and points <prefix>/bin/arena at one of
// them, so a process that is already running keeps its own version.
const (
	defaultReleaseBaseURL = "https://github.com/Kayyo321/agent-arena/releases"
	releaseSkillPath      = ".agents/skills/arena-orchestrator/SKILL.md"
	maxArchiveBytes       = 512 << 20
	updateCheckInterval   = 24 * time.Hour
)

// updateCheckTimeout is a variable so tests can shorten it.
var updateCheckTimeout = 2 * time.Second

func releaseBaseURL() string {
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("ARENA_RELEASE_BASE_URL")), "/"); v != "" {
		return v
	}
	return defaultReleaseBaseURL
}

type semver struct {
	core [3]int
	pre  []string
}

var semverPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

func parseSemver(s string) (semver, bool) {
	if !semverPattern.MatchString(s) {
		return semver{}, false
	}
	core, pre, hasPre := strings.Cut(s, "-")
	var v semver
	for i, part := range strings.Split(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return semver{}, false
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
	}
	return v, true
}

// normalizeVersion strips a leading v and rejects anything that is not a
// semantic version, so a version is always safe in a URL and a directory name.
func normalizeVersion(s string) (string, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if _, ok := parseSemver(s); !ok {
		return "", fmt.Errorf("%q is not a version like 1.2.3", s)
	}
	return s, nil
}

// compareSemver orders versions; a prerelease sorts below its release.
func compareSemver(a, b semver) int {
	for i := range a.core {
		if c := cmp.Compare(a.core[i], b.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, xerr := strconv.ParseUint(a.pre[i], 10, 64)
		y, yerr := strconv.ParseUint(b.pre[i], 10, 64)
		var c int
		switch {
		case xerr == nil && yerr == nil:
			c = cmp.Compare(x, y)
		case xerr == nil:
			c = -1
		case yerr == nil:
			c = 1
		default:
			c = strings.Compare(a.pre[i], b.pre[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a.pre), len(b.pre))
}

func versionNewer(candidate, current string) bool {
	a, okA := parseSemver(candidate)
	b, okB := parseSemver(current)
	return okA && okB && compareSemver(a, b) > 0
}

// managedInstall is an arena that lives in <prefix>/lib/arena/<version>/arena.
type managedInstall struct {
	Prefix  string
	Version string
	Store   string
}

func (m managedInstall) libRoot() string { return filepath.Join(m.Prefix, "lib", "arena") }
func (m managedInstall) link() string    { return filepath.Join(m.Prefix, "bin", "arena") }
func (m managedInstall) manifestPath() string {
	return filepath.Join(m.Prefix, "share", "arena", "install.json")
}

// linkError says why <prefix>/bin/arena is not a symlink to m.Store, or is nil.
func (m managedInstall) linkError() error {
	if info, err := os.Lstat(m.link()); err != nil || info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s is not a symlink", m.link())
	}
	if target, err := filepath.EvalSymlinks(m.link()); err != nil || target != m.Store {
		return fmt.Errorf("%s does not point at %s", m.link(), m.Store)
	}
	return nil
}

// runningExecutable is this process's binary with symlinks resolved, so a
// bin/arena link leads to its store path.
func runningExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return pinnableExecutable(exe), nil
}

func managedInstallAt(exe string) (managedInstall, error) {
	versionDir := filepath.Dir(exe)
	root := filepath.Dir(versionDir)
	lib := filepath.Dir(root)
	if _, ok := parseSemver(filepath.Base(versionDir)); ok && filepath.Base(exe) == "arena" && filepath.Base(root) == "arena" && filepath.Base(lib) == "lib" {
		return managedInstall{Prefix: filepath.Dir(lib), Version: filepath.Base(versionDir), Store: exe}, nil
	}
	return managedInstall{}, fmt.Errorf("%s is not an installer-managed arena (expected <prefix>/lib/arena/<version>/arena); update and uninstall only manage installs made by the install script", exe)
}

// installRecord is <prefix>/share/arena/install.json. scripts/install.sh
// writes it first; update keeps it current and uninstall removes it.
type installRecord struct {
	SchemaVersion int    `json:"schemaVersion"`
	Prefix        string `json:"prefix"`
	Version       string `json:"version"`
	StorePath     string `json:"storePath"`
	BinarySHA256  string `json:"binarySha256"`
	Source        string `json:"source"`
	InstalledAt   string `json:"installedAt"`
}

// samePath reports whether a and b name the same location, comparing cleaned
// paths first and then with symlinks resolved.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// verifiedInstall is managedInstallAt plus proof that the prefix it derived from
// the resolved executable is the one the installer set up: its install.json names
// this prefix and this binary, and <prefix>/bin/arena is a symlink to this
// binary. A symlinked lib/ or a stray copy under lib/arena/ fails these, so
// update and uninstall never write or remove outside the install they were run from.
func verifiedInstall(exe string) (managedInstall, error) {
	exe = pinnableExecutable(exe)
	in, err := managedInstallAt(exe)
	if err != nil {
		return managedInstall{}, err
	}
	refuse := func(format string, args ...any) error {
		return fmt.Errorf("%s is not a verified install of %s: %s; nothing was changed (re-run the install script to repair it)", exe, in.Prefix, fmt.Sprintf(format, args...))
	}
	var rec installRecord
	if err := ReadJSON(in.manifestPath(), &rec); err != nil {
		return managedInstall{}, refuse("cannot read %s: %v", in.manifestPath(), err)
	}
	if rec.SchemaVersion != 1 {
		return managedInstall{}, refuse("%s has schemaVersion %d, expected 1", in.manifestPath(), rec.SchemaVersion)
	}
	if rec.Prefix == "" || !samePath(rec.Prefix, in.Prefix) {
		return managedInstall{}, refuse("%s says the prefix is %q", in.manifestPath(), rec.Prefix)
	}
	if stored, err := filepath.EvalSymlinks(rec.StorePath); err != nil || stored != exe {
		return managedInstall{}, refuse("%s says the binary is %q", in.manifestPath(), rec.StorePath)
	}
	if err := in.linkError(); err != nil {
		return managedInstall{}, refuse("%v", err)
	}
	return in, nil
}

// latestRelease reads the version from GET <base>/latest, which redirects to
// <base>/tag/vX.Y.Z. It stops at the redirect and never fetches the tag page.
func latestRelease(ctx context.Context, base string) (string, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	next := base + "/latest"
	for range 5 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, next, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		location := resp.Header.Get("Location")
		if resp.StatusCode < 300 || resp.StatusCode > 399 || location == "" {
			return "", fmt.Errorf("%s did not lead to a release (%s)", next, resp.Status)
		}
		u, err := resp.Request.URL.Parse(location)
		if err != nil {
			return "", err
		}
		if v, err := normalizeVersion(path.Base(u.Path)); err == nil {
			return v, nil
		}
		next = u.String()
	}
	return "", fmt.Errorf("could not find the latest release under %s", base)
}

func download(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", url, limit)
	}
	return b, nil
}

func releasePlatform() (goos, goarch string, err error) {
	if (runtime.GOOS == "darwin" || runtime.GOOS == "linux") && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		return runtime.GOOS, runtime.GOARCH, nil
	}
	return "", "", fmt.Errorf("no release for %s/%s; releases cover darwin and linux on amd64 and arm64", runtime.GOOS, runtime.GOARCH)
}

// checksumFor finds name in a SHA256SUMS file ("<hex>  <name>" per line).
func checksumFor(sums []byte, name string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// fetchRelease downloads the archive for version, verifies it against
// SHA256SUMS and returns the arena binary plus the skill file the release ships.
func fetchRelease(ctx context.Context, base, version string) (binary, skill []byte, err error) {
	goos, goarch, err := releasePlatform()
	if err != nil {
		return nil, nil, err
	}
	name := fmt.Sprintf("arena_%s_%s_%s.tar.gz", version, goos, goarch)
	root := base + "/download/v" + version + "/"
	archive, err := download(ctx, root+name, maxArchiveBytes)
	if err != nil {
		return nil, nil, err
	}
	sums, err := download(ctx, root+"SHA256SUMS", 1<<20)
	if err != nil {
		return nil, nil, err
	}
	want, ok := checksumFor(sums, name)
	if !ok {
		return nil, nil, fmt.Errorf("SHA256SUMS has no entry for %s; refusing to install", name)
	}
	if got := Hash(archive); got != want {
		return nil, nil, fmt.Errorf("checksum mismatch for %s (SHA256SUMS says %s, downloaded %s); refusing to install", name, want, got)
	}
	return extractRelease(archive)
}

// extractRelease reads the root `arena` binary from a release archive and, when
// present, the skill file the release ships.
func extractRelease(archive []byte) (binary, skill []byte, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		switch path.Clean(h.Name) {
		case "arena":
			binary, err = io.ReadAll(io.LimitReader(tr, maxArchiveBytes))
		case releaseSkillPath:
			skill, err = io.ReadAll(io.LimitReader(tr, 1<<20))
		}
		if err != nil {
			return nil, nil, err
		}
	}
	if len(binary) == 0 {
		return nil, nil, fmt.Errorf("the archive has no arena binary at its root")
	}
	return binary, skill, nil
}

// smokeTest runs the new binary's `version` before anything points at it.
func smokeTest(ctx context.Context, bin, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = append(os.Environ(), "ARENA_NO_UPDATE_CHECK=1")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("the new binary failed `%s version`: %w", bin, err)
	}
	if !Has(strings.Fields(string(out)), version) {
		return fmt.Errorf("the new binary reported %q, expected version %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// switchLink points <prefix>/bin/arena at store by renaming a temporary
// symlink over it, so the link is never missing. It only replaces a link that
// already points at the running binary; creating one is the install script's job.
func switchLink(in managedInstall, store string) error {
	if err := in.linkError(); err != nil {
		return err
	}
	link := in.link()
	tmp := filepath.Join(filepath.Dir(link), ".arena-link-"+ID(""))
	if err := os.Symlink(store, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// liveWork lists the live runs of the project containing dir and the arena-*
// tmux sessions. Both are best effort: no project or no tmux means none.
func liveWork(ctx context.Context, dir string) (p *Project, runs, sessions []string) {
	if opened, err := Open(dir); err == nil {
		p = opened
		ids, _ := p.Runs()
		for _, id := range ids {
			if RunAlive(p, id) {
				runs = append(runs, id)
			}
		}
	}
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return p, runs, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return p, runs, nil
	}
	for _, name := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(name, "arena-") {
			sessions = append(sessions, name)
		}
	}
	return p, runs, sessions
}

// userSkillLocations are the only paths `arena skill install --user` writes:
// one per known agent under the home directory.
func userSkillLocations(home string) map[string]bool {
	locations := map[string]bool{}
	for agent, rel := range skillPaths {
		locations[filepath.Join(home, userSkillPath(agent, rel))] = true
	}
	return locations
}

// trackedSkillCopies returns the manifest entries uninstall and update may act
// on. The manifest is a plain file in the user's state directory, so an entry
// counts only when its path is exactly where arena installs a user skill; every
// other entry is reported in ignored and never touched.
func trackedSkillCopies() (entries []userSkillEntry, ignored []string, err error) {
	m, err := loadUserSkills()
	if err != nil {
		return nil, nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	locations := userSkillLocations(home)
	seen := map[string]bool{}
	for _, e := range m.Skills {
		clean := filepath.Clean(e.Path)
		switch {
		case !filepath.IsAbs(e.Path) || !locations[clean]:
			ignored = append(ignored, fmt.Sprintf("ignored user skill manifest entry %q: not a location arena installs user skills to", e.Path))
		case !seen[clean]:
			seen[clean] = true
			entries = append(entries, userSkillEntry{Path: clean, SHA256: e.SHA256})
		}
	}
	return entries, ignored, nil
}

// readPlainFile reads path only if it is a regular file reached without
// following a symlink below home. Uninstall calls it right before deleting, so
// a swap made after the plan was printed cannot redirect the delete.
func readPlainFile(home, path string) ([]byte, error) {
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%s is not under %s", path, home)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := home
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return nil, err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return nil, fmt.Errorf("%s is a symlink", cur)
		case i < len(parts)-1 && !info.IsDir():
			return nil, fmt.Errorf("%s is not a directory", cur)
		case i == len(parts)-1 && !info.Mode().IsRegular():
			return nil, fmt.Errorf("%s is not a regular file", cur)
		}
	}
	return os.ReadFile(path)
}

// removeSkillCopy deletes a skill copy after re-checking it with readPlainFile.
// A non-empty recorded hash must still match the content; a non-empty backup
// path receives the content before the file goes.
func removeSkillCopy(home, path, recorded, backup string) error {
	b, err := readPlainFile(home, path)
	if err != nil {
		return err
	}
	if recorded != "" && Hash(b) != recorded {
		return fmt.Errorf("%s changed since the plan was made; left alone", path)
	}
	if backup != "" {
		if err := Atomic(backup, b, 0o600); err != nil {
			return err
		}
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	os.Remove(filepath.Dir(path))
	return nil
}

// update installs a release next to the current one and switches bin/arena to it.
func (cli *CLI) update(ctx context.Context, exe, dir string, o Options) error {
	in, err := verifiedInstall(exe)
	if err != nil {
		return err
	}
	explicit := o.Get("to") != ""
	if explicit && o.Bool("check") {
		return fmt.Errorf("--check looks up the latest release; drop --to")
	}
	current := Version
	cur, ok := parseSemver(current)
	if !ok {
		return fmt.Errorf("this build reports version %q, which is not a release version", current)
	}
	base := releaseBaseURL()
	var target string
	if explicit {
		if target, err = normalizeVersion(o.Get("to")); err != nil {
			return err
		}
	} else {
		lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
		target, err = latestRelease(lookup, base)
		cancel()
		if err != nil {
			return fmt.Errorf("look up the latest release: %w", err)
		}
	}
	next, _ := parseSemver(target)
	order := compareSemver(next, cur)
	if o.Bool("check") {
		fmt.Fprintf(cli.Out, "current: %s\nlatest:  %s\n", current, target)
		switch {
		case order > 0:
			fmt.Fprintln(cli.Out, "An update is available. Run: arena update")
		case order == 0:
			fmt.Fprintln(cli.Out, "arena is up to date.")
		default:
			fmt.Fprintln(cli.Out, "This build is newer than the latest release.")
		}
		return nil
	}
	if order == 0 {
		fmt.Fprintf(cli.Out, "arena %s is already installed.\n", current)
		return nil
	}
	if order < 0 && !explicit {
		fmt.Fprintf(cli.Out, "arena %s is newer than the latest release %s; nothing to do. Use --to %s to go back.\n", current, target, target)
		return nil
	}
	if order < 0 {
		fmt.Fprintln(cli.Err, "Warning: a newer arena may have written project state that this older version cannot read.")
	}
	_, runs, sessions := liveWork(ctx, dir)
	for _, run := range runs {
		fmt.Fprintf(cli.Err, "Warning: run %s is live in this project; it keeps running arena %s until it ends.\n", run, current)
	}
	for _, session := range sessions {
		fmt.Fprintf(cli.Err, "Warning: tmux session %s exists; panes it starts later may run arena %s.\n", session, current)
	}
	verb := "Update"
	if order < 0 {
		verb = "Downgrade"
	}
	if err := cli.confirm(fmt.Sprintf("%s arena %s to %s", verb, current, target), o); err != nil {
		return err
	}

	// A version already in the store is reused: replacing its file could pull
	// the binary out from under a process that runs it.
	store := filepath.Join(in.libRoot(), target, "arena")
	if info, err := os.Lstat(filepath.Dir(store)); err == nil && !info.IsDir() {
		return fmt.Errorf("%s is not a plain directory; remove it and retry", filepath.Dir(store))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var skill []byte
	placed := false
	if _, err := os.Stat(store); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(cli.Out, "Downloading arena %s\n", target)
		binary, shipped, err := fetchRelease(ctx, base, target)
		if err != nil {
			return err
		}
		skill = shipped
		if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
			return err
		}
		if err := Atomic(store, binary, 0o755); err != nil {
			return err
		}
		placed = true
	} else if err != nil {
		return err
	}
	// Only a version this run placed is cleaned up. One that was already in the
	// store may be in use, and os.Remove leaves a directory that is not empty.
	discard := func() {
		if placed {
			os.Remove(store)
			os.Remove(filepath.Dir(store))
		}
	}
	if err := smokeTest(ctx, store, target); err != nil {
		discard()
		return err
	}
	installed, err := os.ReadFile(store)
	if err != nil {
		discard()
		return err
	}
	if err := switchLink(in, store); err != nil {
		discard()
		return err
	}
	manifest := installRecord{SchemaVersion: 1, Prefix: in.Prefix, Version: target, StorePath: store, BinarySHA256: Hash(installed), Source: "release", InstalledAt: time.Now().UTC().Format(time.RFC3339)}
	if writeErr := Atomic(in.manifestPath(), JSON(manifest), 0o644); writeErr != nil {
		return fmt.Errorf("arena %s is installed and %s points at it, but %s could not be updated: %w", target, in.link(), in.manifestPath(), writeErr)
	}
	fmt.Fprintf(cli.Out, "arena updated from %s to %s\n", current, target)

	// Only a release downloaded now carries its own skill. The running binary's
	// embedded skill is the old one, so a reused version is not compared against it.
	if skill == nil {
		entries, _, _ := trackedSkillCopies()
		for _, e := range entries {
			if _, err := os.Lstat(e.Path); err == nil {
				fmt.Fprintf(cli.Out, "arena %s was already in the store, so its skill was not compared with your user skill copies; to refresh them run: arena skill install --agent TARGET --user --replace (TARGET is one of `arena skill targets`, or all)\n", target)
				break
			}
		}
		return nil
	}
	if stale := staleSkillCopies(skill); len(stale) > 0 {
		fmt.Fprintln(cli.Out, "These user skill copies are unmodified but differ from this release's skill:")
		for _, path := range stale {
			fmt.Fprintln(cli.Out, "  "+path)
		}
		fmt.Fprintln(cli.Out, "Refresh them with: arena skill install --agent TARGET --user --replace (TARGET is one of `arena skill targets`, or all)")
	}
	return nil
}

// staleSkillCopies are tracked copies the user has not edited that no longer
// match skill, so replacing them loses nothing.
func staleSkillCopies(skill []byte) []string {
	entries, _, err := trackedSkillCopies()
	if err != nil {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var stale []string
	for _, e := range entries {
		if b, err := readPlainFile(home, e.Path); err == nil && Hash(b) == e.SHA256 && Hash(b) != Hash(skill) {
			stale = append(stale, e.Path)
		}
	}
	return stale
}

// uninstall removes the managed install and the user skill copies that are
// still exactly what arena wrote. It never touches a project's .arena/, the
// .gitignore lines, or provider state; those are listed for the user instead.
func (cli *CLI) uninstall(ctx context.Context, exe, dir string, o Options) error {
	in, err := verifiedInstall(exe)
	if err != nil {
		return err
	}
	force, dry := o.Bool("force"), o.Bool("dry-run")
	p, runs, sessions := liveWork(ctx, dir)
	blocked := len(runs) > 0 && !force
	if blocked && !dry {
		return fmt.Errorf("refusing to uninstall while runs are live in this project (%s); stop them, or pass --force", strings.Join(runs, ", "))
	}

	type step struct {
		what string
		do   func() error
	}
	var steps []step
	var kept []string
	var notes []string
	steps = append(steps, step{"remove " + in.link(), func() error { return os.Remove(in.link()) }})
	steps = append(steps, step{"remove " + in.libRoot(), func() error { return os.RemoveAll(in.libRoot()) }})
	if _, err := os.Lstat(in.manifestPath()); err == nil {
		steps = append(steps, step{"remove " + in.manifestPath(), func() error {
			err := os.Remove(in.manifestPath())
			os.Remove(filepath.Dir(in.manifestPath()))
			return err
		}})
	}

	state, stateErr := stateDir()
	backups := ""
	if stateErr == nil {
		backups = filepath.Join(state, "skill-backups")
	}
	copies, ignored, err := trackedSkillCopies()
	if err != nil {
		notes = append(notes, "user skill copies were not checked: "+err.Error())
	}
	notes = append(notes, ignored...)
	home, _ := os.UserHomeDir()
	for _, e := range copies {
		path, recorded := e.Path, e.SHA256
		b, readErr := readPlainFile(home, path)
		switch {
		case errors.Is(readErr, os.ErrNotExist):
			continue
		case readErr != nil:
			kept = append(kept, path+" ("+readErr.Error()+")")
		case Hash(b) == recorded:
			steps = append(steps, step{"remove skill copy " + path, func() error { return removeSkillCopy(home, path, recorded, "") }})
		case force && backups != "":
			backup := filepath.Join(backups, ID("skill-")+".md")
			steps = append(steps, step{"back up modified skill copy " + path + " to " + backup + ", then remove it", func() error { return removeSkillCopy(home, path, "", backup) }})
		default:
			kept = append(kept, path+" (modified; --force backs it up and removes it)")
		}
	}
	if stateErr == nil {
		for _, name := range []string{"user-skills.json", "update-check.json"} {
			file := filepath.Join(state, name)
			if _, err := os.Lstat(file); err == nil {
				steps = append(steps, step{"remove " + file, func() error {
					err := os.Remove(file)
					os.Remove(state)
					return err
				}})
			}
		}
	}

	if dry {
		fmt.Fprintf(cli.Out, "Dry run: uninstalling arena %s from %s would:\n", in.Version, in.Prefix)
	} else {
		fmt.Fprintf(cli.Out, "Uninstalling arena %s from %s will:\n", in.Version, in.Prefix)
	}
	for _, s := range steps {
		fmt.Fprintln(cli.Out, "  "+s.what)
	}
	if blocked {
		fmt.Fprintf(cli.Out, "A real uninstall would be refused: runs are live in this project (%s). --force overrides.\n", strings.Join(runs, ", "))
	}
	for _, n := range notes {
		fmt.Fprintln(cli.Out, "  note: "+n)
	}
	if !dry {
		if err := cli.confirm("Uninstall arena", o); err != nil {
			return err
		}
		var failures []error
		for _, s := range steps {
			if err := s.do(); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", s.what, err))
			}
		}
		if len(failures) > 0 {
			return errors.Join(failures...)
		}
		fmt.Fprintln(cli.Out, "arena is uninstalled.")
	}

	if dry {
		fmt.Fprintln(cli.Out, "Would leave in place (remove by hand if you want them gone):")
	} else {
		fmt.Fprintln(cli.Out, "Left in place (remove by hand if you want them gone):")
	}
	for _, k := range kept {
		fmt.Fprintln(cli.Out, "  skill copy "+k)
	}
	fmt.Fprintln(cli.Out, "  every project's .arena/ directory, which holds its workflows and run history")
	if p != nil {
		if _, err := os.Stat(p.Path()); err == nil {
			fmt.Fprintln(cli.Out, "    this project: "+p.Path())
		}
		for _, rel := range projectSkillPaths() {
			if _, err := os.Lstat(filepath.Join(p.Root, rel)); err == nil {
				fmt.Fprintln(cli.Out, "  project skill copy "+filepath.Join(p.Root, rel))
			}
		}
		fmt.Fprintln(cli.Out, "  .gitignore lines in "+p.Root+": .arena/.state/, .arena/locks/, .arena-root")
	}
	for _, session := range sessions {
		fmt.Fprintln(cli.Out, "  tmux session: tmux kill-session -t "+session)
	}
	return nil
}

func projectSkillPaths() []string {
	seen := map[string]bool{}
	var paths []string
	for _, rel := range skillPaths {
		if !seen[rel] {
			seen[rel] = true
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	return paths
}

type updateCheckCache struct {
	CheckedAt string `json:"checkedAt"`
	Latest    string `json:"latest"`
}

// updateNotice decides whether to look up the latest release and what to
// print. It looks at most once per updateCheckInterval and prints only on the
// run that looked, so the notice shows at most once per interval. A failed
// lookup still counts, so an offline machine is not made to wait on every
// command. It returns the line to print, or "".
func updateNotice(ctx context.Context, cachePath, current string, now time.Time, lookup func(context.Context) (string, error)) string {
	var cache updateCheckCache
	_ = ReadJSON(cachePath, &cache)
	if at, err := time.Parse(time.RFC3339, cache.CheckedAt); err == nil {
		if age := now.Sub(at); age >= 0 && age < updateCheckInterval {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(ctx, updateCheckTimeout)
	defer cancel()
	latest, err := lookup(ctx)
	if err != nil {
		latest = cache.Latest
	}
	_ = Atomic(cachePath, JSON(updateCheckCache{CheckedAt: now.UTC().Format(time.RFC3339), Latest: latest}), 0o644)
	if err != nil || !versionNewer(latest, current) {
		return ""
	}
	return fmt.Sprintf("🌱 arena %s is out (you're on %s). Update with: arena update", latest, current)
}

// updateNoticeEligible filters commands by what they are, before any
// terminal or install check. mcp, rpc and bridge speak a protocol on stdio;
// update and uninstall already say where the install stands.
func updateNoticeEligible(pos []string, o Options, getenv func(string) string) bool {
	if getenv("ARENA_NO_UPDATE_CHECK") == "1" || o.Bool("json") {
		return false
	}
	return len(pos) == 0 || !Has([]string{"mcp", "rpc", "bridge", "update", "uninstall"}, pos[0])
}

// noticeUpdate prints the update notice to stderr after a successful command.
// It never fails the command.
func (cli *CLI) noticeUpdate(ctx context.Context, args []string) {
	pos, o, err := parseArgs(args)
	if err != nil || !updateNoticeEligible(pos, o, os.Getenv) {
		return
	}
	f, ok := cli.Err.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return
	}
	exe, err := runningExecutable()
	if err != nil {
		return
	}
	if _, err := verifiedInstall(exe); err != nil {
		return
	}
	state, err := stateDir()
	if err != nil {
		return
	}
	line := updateNotice(ctx, filepath.Join(state, "update-check.json"), Version, time.Now(), func(ctx context.Context) (string, error) {
		return latestRelease(ctx, releaseBaseURL())
	})
	if line != "" {
		fmt.Fprintln(cli.Err, line)
	}
}
