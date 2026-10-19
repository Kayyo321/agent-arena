package arena

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Everything here is contract coverage for the OpenCode adapter. No test here ran
// or claims live inference; the opt-in TestLiveOpenCode lives in live_test.go with
// the other live tests. Where each fixture comes from:
//   - testdata/opencode-models-verbose-1.19.3.txt: six blocks trimmed from a
//     recording of `opencode models --verbose` on the real 1.19.3 binary, plus one
//     SYNTHETIC block, opencode/truncated-example, which is not in any recording:
//     it was cut short by hand so the parser has an unparseable block to skip.
//   - openCodeAuthEnvOnly: recorded from `opencode auth list` on 1.19.3 (offline,
//     isolated HOME and XDG_*, ANTHROPIC_API_KEY set), with the CLI's box-drawing
//     marks and its path written as stand-ins.
//   - openCodeAuthSignedOut: the Credentials half of that recording. Its no
//     environment variables case (no Environment section) follows from the CLI
//     source, which prints that section only when a variable is set; it was not
//     recorded as a run of its own.
//   - openCodeAuthSignedIn and openCodeAuthSignedInAndEnv: built from the bundled
//     1.19.3 source, not recorded: recording one needs a stored sign-in.
//   - the run streams: built from the event shapes in the bundled 1.19.3 source.
//   - the opencode-debug-agent-build fixtures: the output of
//     `opencode debug agent build` on the real 1.19.3 binary, offline and with an
//     isolated HOME and XDG_* (the data directory rewritten to /home/arena/.local/share).

const openCodeFixture = "testdata/opencode-models-verbose-1.19.3.txt"

func openCodeFixtureText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(openCodeFixture)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// openCodeFake installs a fake opencode on PATH. Besides fakeCLI's argv and stdin
// records it captures, under capture, the working directory, the environment the
// adapter sets and one line per call.
func openCodeFake(t *testing.T, body string) (record, capture string) {
	t.Helper()
	capture = t.TempDir()
	t.Setenv("OPENCODE_FAKE_CAPTURE", capture)
	prelude := `pwd > "$OPENCODE_FAKE_CAPTURE/cwd"
printf '%s\n' "$*" >> "$OPENCODE_FAKE_CAPTURE/calls"
printf '%s' "$OPENCODE_CONFIG_CONTENT" > "$OPENCODE_FAKE_CAPTURE/config"
printf '%s' "${OPENCODE_PERMISSION-unset}" > "$OPENCODE_FAKE_CAPTURE/permission"
printf '%s' "${OPENCODE_DISABLE_PROJECT_CONFIG-unset}" > "$OPENCODE_FAKE_CAPTURE/projectconfig"
printf '%s' "${OPENCODE_DISABLE_AUTOUPDATE-unset}" > "$OPENCODE_FAKE_CAPTURE/autoupdate"
printf '%s' "${OPENCODE_PURE-unset}" > "$OPENCODE_FAKE_CAPTURE/pure"
printf '%s' "${OPENCODE_DISABLE_LSP_DOWNLOAD-unset}" > "$OPENCODE_FAKE_CAPTURE/lspdownload"
printf '%s\t%s\t%s\t%s\n' "$*" "${OPENCODE_PERMISSION-unset}" "${OPENCODE_PURE-unset}" "$OPENCODE_CONFIG_CONTENT" >> "$OPENCODE_FAKE_CAPTURE/envs"
`
	return fakeCLI(t, "opencode", prelude+body), capture
}

// openCodeCallEnv is what one fake opencode call received in OPENCODE_PERMISSION,
// OPENCODE_PURE and OPENCODE_CONFIG_CONTENT.
type openCodeCallEnv struct{ args, permission, pure, config string }

// openCodeCallEnvs lists, in call order, the configuration each call received. The
// per-file records above keep only the last call.
func openCodeCallEnvs(t *testing.T, capture string) []openCodeCallEnv {
	t.Helper()
	calls := []openCodeCallEnv{}
	for _, line := range strings.Split(strings.TrimSuffix(recorded(t, capture, "envs"), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			t.Fatalf("env record %q", line)
		}
		calls = append(calls, openCodeCallEnv{fields[0], fields[1], fields[2], fields[3]})
	}
	return calls
}

// openCodeWorkerConfig is the OPENCODE_CONFIG_CONTENT a worker is started with.
func openCodeWorkerConfig(t *testing.T, write bool) string {
	t.Helper()
	for _, kv := range openCodeEnv(write) {
		if config, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			return config
		}
	}
	t.Fatal("the worker environment carries no OPENCODE_CONFIG_CONTENT")
	return ""
}

func openCodeNoCall(t *testing.T, capture string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(capture, "calls")); err == nil {
		t.Fatalf("opencode must not be started: %s", recorded(t, capture, "calls"))
	}
}

// Recorded from opencode 1.19.3 `auth list` on a sandbox with no stored sign-in:
// the wording, the counts and the dim ANSI code before the path and the variable
// name come from the recording, which was piped. The box-drawing marks of the real
// output are written here as T, |, the dash and the bullet, and the path is
// shortened; the parser reads only the counts. See the provenance list above for
// how openCodeAuthSignedOut relates to the recording.
const openCodeAuthSignedOut = "\x1b[0m\nT  Credentials \x1b[90m~/.local/share/opencode/auth.json\n|\n\u2014  0 credentials\n"
const openCodeAuthEnvOnly = openCodeAuthSignedOut + "\nT  Environment\n|\n\u2022  Anthropic \x1b[90mANTHROPIC_API_KEY\n|\n\u2014  1 environment variable\n"

// The CLI's source prints every count with "credentials", so one stored sign-in
// reads "1 credentials". These two are built from that source, not recorded.
const openCodeAuthSignedIn = "\x1b[0m\nT  Credentials \x1b[90m~/.local/share/opencode/auth.json\n|\n\u2022  OpenCode Zen \x1b[90mapi\n|\n\u2014  1 credentials\n"
const openCodeAuthSignedInAndEnv = openCodeAuthSignedIn + "\nT  Environment\n|\n\u2022  Anthropic \x1b[90mANTHROPIC_API_KEY\n|\n\u2022  GitHub Copilot \x1b[90mGITHUB_TOKEN\n|\n\u2014  2 environment variables\n"

func TestOpenCodeAuthListParsesOnlyCounts(t *testing.T) {
	for name, want := range map[string][3]any{
		openCodeAuthSignedOut:      {0, 0, true},
		openCodeAuthEnvOnly:        {0, 1, true},
		openCodeAuthSignedIn:       {1, 0, true},
		openCodeAuthSignedInAndEnv: {1, 2, true},
		"Error: something else\n":  {0, 0, false},
		"":                         {0, 0, false},
	} {
		credentials, environment, ok := parseOpenCodeAuth(name)
		if credentials != want[0] || environment != want[1] || ok != want[2] {
			t.Fatalf("%q -> %d %d %v, want %v", name, credentials, environment, ok, want)
		}
	}
}

// Every opencode call runs without external plugins, whatever the parent sets: the
// worker's --pure and the probes' and catalog's OPENCODE_PURE=1 must agree.
func TestOpenCodeEnvDisablesPluginsOnce(t *testing.T) {
	for _, parent := range []string{"0", "false", "1", ""} {
		t.Setenv("OPENCODE_PURE", parent)
		for _, write := range []bool{false, true} {
			values := []string{}
			for _, kv := range openCodeEnv(write) {
				if value, ok := strings.CutPrefix(kv, "OPENCODE_PURE="); ok {
					values = append(values, value)
				}
			}
			if !reflect.DeepEqual(values, []string{"1"}) {
				t.Fatalf("parent %q, writer %v: OPENCODE_PURE entries %q, want one \"1\"", parent, write, values)
			}
		}
	}
}

// A worker reads files, so its session must never reach OpenCode's share service.
// Every opencode call drops the parent's OPENCODE_AUTO_SHARE and carries share
// "disabled" in OPENCODE_CONFIG_CONTENT, also over a parent config that sets share
// to auto. `opencode debug config` showed on 1.19.3 that this key and value resolve
// to share "disabled" (see openCodeShareKey); the test pins what Arena puts in the
// environment, which is contract coverage and not a run of OpenCode.
func TestOpenCodeEnvDisablesSharing(t *testing.T) {
	for _, parent := range []string{"1", "true", "0", ""} {
		t.Setenv("OPENCODE_AUTO_SHARE", parent)
		t.Setenv("OPENCODE_CONFIG_CONTENT", `{"share":"auto","autoshare":true}`)
		for _, write := range []bool{false, true} {
			for _, kv := range openCodeEnv(write) {
				if name, _, _ := strings.Cut(kv, "="); name == "OPENCODE_AUTO_SHARE" {
					t.Fatalf("parent %q, writer %v: the inherited %q reaches opencode", parent, write, kv)
				}
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(openCodeWorkerConfig(t, write)), &config); err != nil {
				t.Fatal(err)
			}
			if config["share"] != "disabled" {
				t.Fatalf("parent %q, writer %v: share = %v, want \"disabled\"", parent, write, config["share"])
			}
			if _, ok := config["autoshare"]; ok {
				t.Fatalf("writer %v: the config must not set the deprecated autoshare key: %v", write, config)
			}
		}
	}
}

// requireOpenCodeNoLSPOrFormatter holds a captured OPENCODE_CONFIG_CONTENT to the
// two keys that keep language servers and formatters off. Both must be the JSON
// boolean false, not absent: an absent key leaves the user's own lsp or formatter
// setting in force, and 1.19.3 resolved a user-level true (or an object that
// configures servers or formatters) to itself when Arena's config did not set them.
func requireOpenCodeNoLSPOrFormatter(t *testing.T, label string, config map[string]any) {
	t.Helper()
	for _, key := range []string{"lsp", "formatter"} {
		if value, ok := config[key]; !ok || value != false {
			t.Fatalf("%s: %q = %v (present %v), want false: the user's own setting would apply: %v", label, key, value, ok, config)
		}
	}
}

// Language servers and formatters run programs of their own outside any sandbox,
// with the worktree's content, so every opencode call turns them off, also over a
// parent configuration (and so a user-level one) that turns them on in boolean or
// object form, and sets OPENCODE_DISABLE_LSP_DOWNLOAD once whatever the parent
// holds. The offline check on the real 1.19.3 binary is in docs/VALIDATION.md; this
// pins what Arena puts in the environment and is contract coverage.
func TestOpenCodeEnvDisablesLanguageServersAndFormatters(t *testing.T) {
	for _, parent := range []string{
		`{"lsp":true,"formatter":true}`,
		`{"lsp":{"gopls":{"command":["gopls"]}},"formatter":{"prettier":{"command":["prettier","--write","$FILE"]}}}`,
	} {
		for _, download := range []string{"0", "false", "1", ""} {
			t.Setenv("OPENCODE_CONFIG_CONTENT", parent)
			t.Setenv("OPENCODE_DISABLE_LSP_DOWNLOAD", download)
			for _, write := range []bool{false, true} {
				var config map[string]any
				if err := json.Unmarshal([]byte(openCodeWorkerConfig(t, write)), &config); err != nil {
					t.Fatal(err)
				}
				requireOpenCodeNoLSPOrFormatter(t, fmt.Sprintf("parent %s, writer %v", parent, write), config)
				values := []string{}
				for _, kv := range openCodeEnv(write) {
					if value, ok := strings.CutPrefix(kv, "OPENCODE_DISABLE_LSP_DOWNLOAD="); ok {
						values = append(values, value)
					}
				}
				if !reflect.DeepEqual(values, []string{"1"}) {
					t.Fatalf("parent %q, writer %v: OPENCODE_DISABLE_LSP_DOWNLOAD entries %q, want one \"1\"", download, write, values)
				}
			}
		}
	}
}

// A probe or catalog call must not stall on a child that opencode left running
// with the output pipes open: the WaitDelay of openCodeCommandFor closes them
// shortly after opencode exits, and the call reports a failure instead of waiting
// out the probe budget. The fake exits at once and leaves a sleep holding its
// stdout for longer than the test allows.
func TestOpenCodeCallsDoNotWaitForALeftoverChild(t *testing.T) {
	openCodeFake(t, "sleep 6 &\necho 1.19.3\n")
	start := time.Now()
	out, err := openCodeCommand(context.Background(), "--version").Output()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the call waited %v for a child that holds its pipes", elapsed)
	}
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("want exec.ErrWaitDelay, got %v (output %q)", err, out)
	}
	// Reader and writer calls are built by the same function.
	for _, write := range []bool{false, true} {
		if cmd := openCodeCommandFor(context.Background(), write, "models", "--verbose"); cmd.WaitDelay <= 0 || cmd.WaitDelay > 10*time.Second {
			t.Fatalf("writer %v: WaitDelay %v is not a short positive delay", write, cmd.WaitDelay)
		}
	}
}

func TestOpenCodeCatalogParsesRecordedFixture(t *testing.T) {
	models, skipped := parseOpenCodeModels(openCodeFixtureText(t))
	if skipped != 1 {
		t.Fatalf("the truncated block must be skipped and counted, got %d", skipped)
	}
	// Order follows the CLI; efforts are the model's variant names by rank.
	want := []struct{ id, name, efforts string }{
		{"opencode/big-pickle", "Big Pickle", ""},
		{"opencode/cat-jam-free", "Cat Jam Free", "low,medium,high,xhigh,max"},
		{"opencode/nemotron-3.5-super-vl-free", "Nemotron 3.5 Super VL Free", "none,minimal,low,medium,high,xhigh"},
		{"opencode/deepseek-v4.1-flash-free", "DeepSeek V4.1 Flash Free", "low,high,max"},
		{"amazon-bedrock/amazon.nova-2-lite-v1:0", "Nova 2 Lite", "low,medium,high"},
		{"github-copilot/claude-fable-5", "Claude Fable 5", "low,medium,high,xhigh,max"},
	}
	if len(models) != len(want) {
		t.Fatalf("models: %+v", models)
	}
	for i, w := range want {
		if models[i].ID != w.id || models[i].Name != w.name || strings.Join(models[i].Efforts, ",") != w.efforts || models[i].Efforts == nil {
			t.Fatalf("model %d: %+v, want %+v", i, models[i], w)
		}
	}
}

func TestOpenCodeCatalogEdgeCases(t *testing.T) {
	// Variant names are an opaque set: known names sort by rank, others last.
	models, skipped := parseOpenCodeModels("opencode/a\n{\"name\":\"A\",\"variants\":{\"max\":{},\"zeta\":{},\"low\":{},\"alpha\":{},\"none\":{},\"high\":{}}}\n")
	if skipped != 0 || len(models) != 1 || strings.Join(models[0].Efforts, ",") != "none,low,high,max,alpha,zeta" {
		t.Fatalf("effort order: %+v %d", models, skipped)
	}
	// Text before the first header is ignored, a missing name falls back to the
	// ID, a missing variants key means no efforts, CRLF output parses, and a block
	// that is not an object, or has trailing text, is skipped and counted.
	out := "a notice line\n\nopencode/plain\r\n{}\r\nopencode/crlf\r\n{\r\n  \"name\": \"Crlf\"\r\n}\r\nopencode/list\n[1]\nopencode/trailing\n{}\nextra text\nopencode/last\n{\"variants\":{\"high\":{}}}\n"
	models, skipped = parseOpenCodeModels(out)
	if skipped != 2 || len(models) != 3 {
		t.Fatalf("blocks: %+v skipped %d", models, skipped)
	}
	if models[0].ID != "opencode/plain" || models[0].Name != "opencode/plain" || len(models[0].Efforts) != 0 || models[1].Name != "Crlf" || models[2].ID != "opencode/last" || models[2].Efforts[0] != "high" {
		t.Fatalf("blocks: %+v", models)
	}
	// The catalog stops at the cap rather than growing without bound.
	var big strings.Builder
	for i := 0; i < openCodeMaxModels+5; i++ {
		fmt.Fprintf(&big, "p/m%d\n{}\n", i)
	}
	if models, skipped = parseOpenCodeModels(big.String()); len(models) != openCodeMaxModels || skipped != 0 {
		t.Fatalf("cap: %d models, %d skipped", len(models), skipped)
	}
	if models, skipped = parseOpenCodeModels(""); len(models) != 0 || skipped != 0 {
		t.Fatalf("empty output: %v %d", models, skipped)
	}
}

func TestOpenCodeCatalogRunsTheCLI(t *testing.T) {
	t.Setenv("OPENCODE_PURE", "0") // the parent's value must not decide the plugin set
	fixture, err := filepath.Abs(openCodeFixture)
	if err != nil {
		t.Fatal(err)
	}
	record, capture := openCodeFake(t, "cat '"+fixture+"'\n")
	models, err := openCodeCatalog(context.Background())
	if err != nil || len(models) != 6 {
		t.Fatalf("catalog: %d models, %v", len(models), err)
	}
	if args := recorded(t, record, "args"); args != "models\n--verbose\n" {
		t.Fatalf("argv: %q", args)
	}
	// The catalog sees the configuration a worker will see.
	if recorded(t, capture, "projectconfig") != "1" || recorded(t, capture, "autoupdate") != "1" {
		t.Fatal("catalog calls must ignore project config and never update")
	}
	// ... and the plugin set a worker runs with: a plugin's config hook can add a
	// model or variant that opencode run --pure never sees.
	if got := recorded(t, capture, "pure"); got != "1" {
		t.Fatalf("catalog calls must run without external plugins, like the worker: OPENCODE_PURE=%q", got)
	}

	openCodeFake(t, "echo 'boom: no network' >&2\nexit 2\n")
	if _, err := openCodeCatalog(context.Background()); err == nil || !strings.Contains(err.Error(), "could not list models") || !strings.Contains(err.Error(), "boom: no network") {
		t.Fatalf("failed listing: %v", err)
	}

	// Only unparseable blocks: the error carries the count.
	openCodeFake(t, "printf 'opencode/cut\\n{\\n  \"id\": \\n'\n")
	if _, err := openCodeCatalog(context.Background()); err == nil || !strings.Contains(err.Error(), "1 unparseable") {
		t.Fatalf("all blocks skipped: %v", err)
	}
	openCodeFake(t, "")
	if _, err := openCodeCatalog(context.Background()); err == nil || !strings.Contains(err.Error(), "0 unparseable") {
		t.Fatalf("empty listing: %v", err)
	}
}

// The probe and the catalog must see the configuration the worker sees. If the
// parent's OPENCODE_CONFIG_CONTENT reached them, a provider, model or variant it
// defines would pass readiness and then be unknown to the worker, and an unknown
// --variant makes opencode run hang. Checked on 1.19.3: with such a value,
// `opencode models --verbose` lists the parent's model. The permission check
// resolves the build agent twice, first in a reader's environment and then in a
// writer's, so each role is judged on the map it will run with. The same holds for
// external plugins, whose config hook can add providers, models and variants and
// change agent.build.permission: the worker runs with --pure, so every probe and
// catalog call runs with OPENCODE_PURE=1 too (checked on 1.19.3 offline, with a
// local plugin; see docs/VALIDATION.md).
func TestOpenCodeProbeAndCatalogRunInTheWorkerConfiguration(t *testing.T) {
	parentConfig := `{"provider":{"myprov":{"models":{"m1":{"variants":{"turbo":{}}}}}},"permission":{"bash":"allow"}}`
	t.Setenv("OPENCODE_CONFIG_CONTENT", parentConfig)
	t.Setenv("OPENCODE_PERMISSION", `{"bash":"allow"}`)
	t.Setenv("OPENCODE_PURE", "0") // the parent cannot turn plugins back on for the probe
	_, capture := openCodeProbeCLI{}.install(t)
	r := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "xhigh"}
	if got := probeOpenCode(context.Background(), r, openCodeReadiness(r)); !got.CanStart {
		t.Fatalf("readiness: %+v", got)
	}
	if _, err := openCodeCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader, writer := openCodeWorkerConfig(t, false), openCodeWorkerConfig(t, true)
	for _, want := range []string{reader, writer} {
		if want == parentConfig || strings.Contains(want, "myprov") {
			t.Fatalf("the worker config must not carry the parent's: %s", want)
		}
	}
	if reader == writer {
		t.Fatal("a reader and a writer must run with different permission maps")
	}
	calls := openCodeCallEnvs(t, capture)
	for _, call := range calls {
		var config map[string]any
		if err := json.Unmarshal([]byte(call.config), &config); err != nil {
			t.Fatalf("opencode %s: %v", call.args, err)
		}
		requireOpenCodeNoLSPOrFormatter(t, "opencode "+call.args, config)
	}
	// The probe's six calls and the catalog's own.
	if len(calls) != 7 {
		t.Fatalf("calls: %+v", calls)
	}
	seen := []string{}
	for _, call := range calls {
		seen = append(seen, call.args)
		if call.config == parentConfig || strings.Contains(call.config, "myprov") {
			t.Fatalf("opencode %s received the parent's OPENCODE_CONFIG_CONTENT: %s", call.args, call.config)
		}
		if call.permission != "unset" {
			t.Fatalf("opencode %s received the parent's OPENCODE_PERMISSION: %s", call.args, call.permission)
		}
		if call.pure != "1" {
			t.Fatalf("opencode %s must run without external plugins, as the worker does: OPENCODE_PURE=%q", call.args, call.pure)
		}
	}
	if want := []string{"--version", "run --help", "auth list", "debug agent build", "debug agent build", "models --verbose", "models --verbose"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("calls: %q, want %q", seen, want)
	}
	// Everything runs as a reader, except the second resolution of the build agent,
	// which runs as a writer.
	for i, call := range calls {
		want := reader
		if i == 4 {
			want = writer
		}
		if call.config != want {
			t.Fatalf("opencode %s (call %d) must run in the worker's configuration:\n%s\nwant\n%s", call.args, i, call.config, want)
		}
	}
}

const openCodeHelpOK = "cat <<'EOF'\nopencode run [message..]\n      --pure\n  -m, --model\n      --format\n      --dir\n      --variant\nEOF"

// The rule lists that `opencode debug agent build` printed on 1.19.3 for Arena's own
// permission map, as a reader and as a writer, and for a reader whose global
// $XDG_CONFIG_HOME/opencode/opencode.json holds
// {"agent":{"build":{"permission":{"*":"allow","codesearch":"allow","mymcp_*":"allow"}}}},
// and the two flipped recordings named below.
const (
	openCodeAgentReader        = "testdata/opencode-debug-agent-build-1.19.3-reader.json"
	openCodeAgentWriter        = "testdata/opencode-debug-agent-build-1.19.3-writer.json"
	openCodeAgentReaderWidened = "testdata/opencode-debug-agent-build-1.19.3-reader-widened.json"
	openCodeAgentWriterWidened = "testdata/opencode-debug-agent-build-1.19.3-writer-widened.json"
	// A writer with a global agent.build.permission {"*":"ask","edit":{"../*":"ask"}},
	// and a reader with {"*":"allow","read":{"*.env":"ask","*":"allow"}}: the user's
	// key order puts Arena's allow after its deny.
	openCodeAgentWriterFlippedEdit = "testdata/opencode-debug-agent-build-1.19.3-writer-flipped-edit.json"
	openCodeAgentReaderFlippedRead = "testdata/opencode-debug-agent-build-1.19.3-reader-flipped-read.json"
	openCodeFixtureDataHome        = "/home/arena/.local/share" // XDG_DATA_HOME of those recordings
	openCodeFixtureToolOutput      = openCodeFixtureDataHome + "/opencode/tool-output/*"
	openCodeProbeThroughAuth       = "--version\nrun --help\nauth list\n"
	openCodeProbeThroughReader     = openCodeProbeThroughAuth + "debug agent build\n"
	openCodeProbeThroughAgent      = openCodeProbeThroughReader + "debug agent build\n"
	openCodeProbeThroughCatalog    = openCodeProbeThroughAgent + "models --verbose\n"
)

// openCodeCat is a fake-CLI body that prints a testdata file.
func openCodeCat(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return "cat '" + path + "'"
}

// openCodeAgentJSON is a fake-CLI body that prints a build agent with the given
// rules, every rule at pattern "*" except the ones given with a pattern.
func openCodeAgentJSON(rules ...openCodeRule) string {
	for i := range rules {
		if rules[i].Pattern == "" {
			rules[i].Pattern = "*"
		}
	}
	return "cat <<'EOF'\n" + compactJSON(map[string]any{"name": "build", "permission": rules}) + "\nEOF"
}

type openCodeProbeCLI struct{ version, help, auth, models, agentReader, agentWriter string }

// install fakes the five read-only calls a probe makes; zero fields answer as a
// healthy, signed-in 1.19.3 with the recorded catalog and the recorded rules of
// Arena's own map, which no user configuration widens. The fake tells the
// reader's call from the writer's by the writer's "../*" edit rule in
// OPENCODE_CONFIG_CONTENT. It also sets XDG_DATA_HOME to the recordings' data
// directory, so the recorded tool-output rule is the one the probe expects.
func (c openCodeProbeCLI) install(t *testing.T) (record, capture string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", openCodeFixtureDataHome)
	for field, fallback := range map[*string]string{&c.version: "echo " + openCodeVersion, &c.help: openCodeHelpOK, &c.auth: openCodeAuthSignedIn, &c.models: openCodeCat(t, openCodeFixture), &c.agentReader: openCodeCat(t, openCodeAgentReader), &c.agentWriter: openCodeCat(t, openCodeAgentWriter)} {
		if *field == "" {
			*field = fallback
		}
	}
	if !strings.HasPrefix(c.auth, "cat <<") && !strings.HasPrefix(c.auth, "exit") && !strings.HasPrefix(c.auth, "echo") {
		c.auth = "cat <<'EOF'\n" + c.auth + "EOF"
	}
	debug := "case \"$OPENCODE_CONFIG_CONTENT\" in\n*'\"../*\"'*) " + c.agentWriter + "\n;;\n*) " + c.agentReader + "\n;;\nesac"
	return openCodeFake(t, "case \"$1\" in\n--version) "+c.version+"\n;;\nrun) "+c.help+"\n;;\nauth) "+c.auth+"\n;;\nmodels) "+c.models+"\n;;\ndebug) "+debug+"\n;;\nesac\n")
}

func openCodeReadiness(r Runtime) Readiness {
	return Readiness{Provider: r.Provider, Model: r.Model, State: "unknown", Auth: "unknown", ModelState: "unknown", Efforts: []string{}}
}

func TestOpenCodeProbeStates(t *testing.T) {
	cat := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "xhigh"}
	cases := []struct {
		name    string
		cli     openCodeProbeCLI
		runtime Runtime
		state   string
		detail  string
		auth    string
		calls   string
	}{
		{"version probe fails", openCodeProbeCLI{version: "exit 1"}, cat, "incompatible version", "version probe failed", "", "--version\n"},
		{"version drift", openCodeProbeCLI{version: "echo 1.20.0"}, cat, "incompatible version", "1.20.0 is not 1.19.3", "", "--version\n"},
		{"flag missing", openCodeProbeCLI{help: "echo 'opencode run --pure --model --format --dir'"}, cat, "incompatible version", "lacks --variant", "", "--version\nrun --help\n"},
		{"help fails", openCodeProbeCLI{help: "exit 1"}, cat, "incompatible version", "lacks --format", "", "--version\nrun --help\n"},
		{"auth list fails", openCodeProbeCLI{auth: "exit 1"}, cat, "unavailable", "opencode auth login", "opencode auth list unavailable", "--version\nrun --help\nauth list\n"},
		{"auth output unrecognised", openCodeProbeCLI{auth: "echo something new"}, cat, "incompatible version", "did not report", "not recognised", "--version\nrun --help\nauth list\n"},
		{"signed out", openCodeProbeCLI{auth: openCodeAuthSignedOut}, cat, "signed out", "opencode auth login", "signed out", "--version\nrun --help\nauth list\n"},
		{"environment only", openCodeProbeCLI{auth: openCodeAuthEnvOnly}, cat, "unknown", "cannot verify them without running inference", "auth unknown, environment variables present", "--version\nrun --help\nauth list\n"},
		{"catalog fails", openCodeProbeCLI{models: "exit 1"}, cat, "unavailable", "could not list models", "", openCodeProbeThroughCatalog},
		{"unknown model", openCodeProbeCLI{}, Runtime{Provider: "opencode", Model: "opencode/not-listed"}, "unavailable model", "absent from opencode models", "", openCodeProbeThroughCatalog},
		{"unknown effort", openCodeProbeCLI{}, Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "ultra"}, "unsupported effort", "makes opencode run hang", "", openCodeProbeThroughCatalog},
		{"effort on a model without variants", openCodeProbeCLI{}, Runtime{Provider: "opencode", Model: "opencode/big-pickle", ReasoningEffort: "high"}, "unsupported effort", "makes opencode run hang", "", openCodeProbeThroughCatalog},
		// The user's own configuration can add tools to a worker, so readiness resolves
		// the build agent for a reader and for a writer and fails closed, before the
		// catalog is read.
		{"reader build agent widened by the user's configuration", openCodeProbeCLI{agentReader: openCodeCat(t, openCodeAgentReaderWidened)}, cat, "unsupported", "codesearch, mymcp_*", "", openCodeProbeThroughAgent},
		{"writer build agent widened by the user's configuration", openCodeProbeCLI{agentWriter: openCodeCat(t, openCodeAgentWriterWidened)}, cat, "unsupported", "codesearch, mymcp_*", "", openCodeProbeThroughAgent},
		{"both build agents widened", openCodeProbeCLI{agentReader: openCodeCat(t, openCodeAgentReaderWidened), agentWriter: openCodeCat(t, openCodeAgentWriterWidened)}, cat, "unsupported", "global agent definition", "", openCodeProbeThroughAgent},
		{"reader gets edit from the user's configuration", openCodeProbeCLI{agentReader: openCodeCat(t, openCodeAgentWriter)}, cat, "unsupported", "allows tools beyond Arena's permission map for workers: edit.", "", openCodeProbeThroughAgent},
		// The pattern rules of read and edit, which the tool check skips: a user's
		// edit or read object can put Arena's allow after its deny.
		{"writer edit allow after the outside deny", openCodeProbeCLI{agentWriter: openCodeCat(t, openCodeAgentWriterFlippedEdit)}, cat, "unsupported", `edit "../x" resolves to allow, not deny, by the rule edit * allow`, "", openCodeProbeThroughAgent},
		{"reader read allow after the .env deny", openCodeProbeCLI{agentReader: openCodeCat(t, openCodeAgentReaderFlippedRead)}, cat, "unsupported", `read ".env" resolves to allow, not deny, by the rule read * allow`, "", openCodeProbeThroughAgent},
		{"build agent resolution fails", openCodeProbeCLI{agentReader: "echo 'Error: config invalid' >&2\nexit 1"}, cat, "unavailable", "opencode debug agent build failed; Error: config invalid", "", openCodeProbeThroughReader},
		{"build agent output unrecognised", openCodeProbeCLI{agentWriter: "echo something new"}, cat, "incompatible version", "did not print a readable permission list", "", openCodeProbeThroughAgent},
		{"writer build agent resolution fails", openCodeProbeCLI{agentWriter: "exit 1"}, cat, "unavailable", "opencode debug agent build failed", "", openCodeProbeThroughAgent},
		{"build agent without rules", openCodeProbeCLI{agentReader: "echo '{\"name\":\"build\",\"permission\":[]}'"}, cat, "incompatible version", "did not print a readable permission list", "", openCodeProbeThroughReader},
		{"build agent rule with an unknown action", openCodeProbeCLI{agentReader: openCodeAgentJSON(openCodeRule{Permission: "read", Action: "maybe"})}, cat, "incompatible version", "did not print a readable permission list", "", openCodeProbeThroughReader},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, capture := c.cli.install(t)
			got := probeOpenCode(context.Background(), c.runtime, openCodeReadiness(c.runtime))
			if got.CanStart || got.State != c.state || !strings.Contains(got.Detail, c.detail) || !strings.Contains(got.Auth, c.auth) {
				t.Fatalf("readiness: %+v", got)
			}
			if calls := recorded(t, capture, "calls"); calls != c.calls {
				t.Fatalf("calls: %q, want %q", calls, c.calls)
			}
		})
	}
}

func TestOpenCodeProbeReportsMissingBinaryAndBadRuntime(t *testing.T) {
	cat := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free"}
	t.Setenv("PATH", t.TempDir())
	if got := probeOpenCode(context.Background(), cat, openCodeReadiness(cat)); got.CanStart || got.State != "missing" || !strings.Contains(got.Detail, "not installed") {
		t.Fatalf("missing binary: %+v", got)
	}
	// An unsupported runtime is refused before opencode starts.
	_, capture := openCodeProbeCLI{}.install(t)
	bad := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", Options: map[string]any{"authMethod": "x"}}
	if got := probeOpenCode(context.Background(), bad, openCodeReadiness(bad)); got.CanStart || got.State != "unsupported" || !strings.Contains(got.Detail, "authMethod") {
		t.Fatalf("bad runtime: %+v", got)
	}
	openCodeNoCall(t, capture)
}

func TestOpenCodeProbeReadyWithoutRunningInference(t *testing.T) {
	for _, c := range []struct {
		runtime Runtime
		efforts string
		auth    string
	}{
		{Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "xhigh"}, "low,medium,high,xhigh,max", openCodeAuthSignedIn},
		{Runtime{Provider: "opencode", Model: "opencode/big-pickle"}, "", openCodeAuthSignedIn},
		// Stored sign-in counts even beside environment variables.
		{Runtime{Provider: "opencode", Model: "opencode/nemotron-3.5-super-vl-free", ReasoningEffort: "none"}, "none,minimal,low,medium,high,xhigh", openCodeAuthSignedInAndEnv},
	} {
		_, capture := openCodeProbeCLI{auth: c.auth}.install(t)
		got := probeOpenCode(context.Background(), c.runtime, openCodeReadiness(c.runtime))
		if !got.CanStart || got.Version != openCodeVersion || strings.Join(got.Efforts, ",") != c.efforts || !strings.Contains(got.ModelState, "inference access unverified") || !strings.Contains(got.Detail, "live inference is unverified") {
			t.Fatalf("readiness: %+v", got)
		}
		// The fixture holds one cut-short block, and the probe says so.
		if !strings.Contains(got.Detail, "1 unparseable catalog blocks skipped") {
			t.Fatalf("skipped blocks must show in the detail: %s", got.Detail)
		}
		// Only the read-only calls ran: never a run with a prompt. The build agent
		// is resolved once as a reader and once as a writer, without a model call.
		if calls := recorded(t, capture, "calls"); calls != openCodeProbeThroughCatalog {
			t.Fatalf("calls: %q", calls)
		}
		if !strings.Contains(got.Detail, "opencode debug agent build shows no further tool allowed") {
			t.Fatalf("the detail must say what was checked: %s", got.Detail)
		}
	}
}

func TestOpenCodeVersionMatchesCompatibilityManifest(t *testing.T) {
	for _, adapter := range Manifest().Adapters {
		if adapter.ID == "opencode" {
			if !Has(adapter.ObservedVersions, openCodeVersion) {
				t.Fatalf("providers/compatibility.json observes %v, the adapter pins %s", adapter.ObservedVersions, openCodeVersion)
			}
			return
		}
	}
	t.Fatal("providers/compatibility.json has no opencode adapter; the pinned version must be recorded there")
}

func TestOpenCodeRuntimeOptionsAndArguments(t *testing.T) {
	ok := Runtime{Provider: "opencode", Model: "opencode/big-pickle"}
	if err := openCodeRuntimeError(ok); err != nil {
		t.Fatal(err)
	}
	ok.ReasoningEffort = "high" // effort is optional
	if err := openCodeRuntimeError(ok); err != nil {
		t.Fatal(err)
	}
	ok.Options = map[string]any{} // an empty options object is no option
	if err := openCodeRuntimeError(ok); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Runtime{
		"authMethod":      {Model: "a/b", Options: map[string]any{"authMethod": "claude-code-sign-in"}},
		"commandApproval": {Model: "a/b", Options: map[string]any{"commandApproval": "sandboxed-blanket"}},
		"unknown key":     {Model: "a/b", Options: map[string]any{"backend": "x"}},
		"effort option":   {Model: "a/b", Options: map[string]any{"effort": "high"}},
		"no provider":     {Model: "big-pickle"},
		"empty model":     {Model: "opencode/"},
		"empty provider":  {Model: "/big-pickle"},
		"flag as model":   {Model: "-m/x"},
		"model blank":     {Model: "opencode/big pickle"},
		"flag as effort":  {Model: "a/b", ReasoningEffort: "--auto"},
		"effort blank":    {Model: "a/b", ReasoningEffort: "hi gh"},
	} {
		r.Provider = "opencode"
		if err := openCodeRuntimeError(r); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	if err := openCodeRuntimeError(Runtime{Provider: "opencode", Model: "a/b", Options: map[string]any{"effort": "high"}}); !strings.Contains(err.Error(), "reasoningEffort") {
		t.Fatalf("the error should say where effort goes: %v", err)
	}
}

// What `opencode run --format json` writes, per the 1.19.3 bundled source: part
// types and field names are from the source, the values are made up. The plain
// first line stands for a notice the CLI may print between frames, which the
// stream reader skips.
const openCodeFixtureStream = `cat <<'EOF'
A plain notice line between frames
{"type":"step_start","timestamp":1,"sessionID":"ses_fixture","part":{"id":"prt_1","sessionID":"ses_fixture","messageID":"msg_1","type":"step-start"}}
{"type":"tool_use","timestamp":2,"sessionID":"ses_fixture","part":{"id":"prt_2","sessionID":"ses_fixture","messageID":"msg_1","type":"tool","callID":"call_1","tool":"read","state":{"status":"completed","input":{"filePath":"a.go"},"output":"x","title":"a.go","metadata":{},"time":{"start":1,"end":2}}}}
{"type":"tool_use","timestamp":3,"sessionID":"ses_fixture","part":{"id":"prt_3","sessionID":"ses_fixture","messageID":"msg_1","type":"tool","callID":"call_2","tool":"grep","state":{"status":"error","input":{},"error":"denied","time":{"start":2,"end":3}}}}
{"type":"step_finish","timestamp":4,"sessionID":"ses_fixture","part":{"id":"prt_4","sessionID":"ses_fixture","messageID":"msg_1","type":"step-finish","reason":"tool-calls","cost":0,"tokens":{"input":10,"output":4,"reasoning":2,"cache":{"read":1,"write":0}}}}
{"type":"step_start","timestamp":5,"sessionID":"ses_fixture","part":{"id":"prt_5","sessionID":"ses_fixture","messageID":"msg_2","type":"step-start"}}
{"type":"text","timestamp":6,"sessionID":"ses_fixture","part":{"id":"prt_6","sessionID":"ses_fixture","messageID":"msg_2","type":"text","text":"{\"summary\":\"fixture\"}","time":{"start":5,"end":6}}}
{"type":"step_finish","timestamp":7,"sessionID":"ses_fixture","part":{"id":"prt_7","sessionID":"ses_fixture","messageID":"msg_2","type":"step-finish","reason":"stop","cost":0,"tokens":{"total":30,"input":20,"output":6,"reasoning":0,"cache":{"read":0,"write":0}}}}
EOF
`

func openCodeInvocation(t *testing.T) Invocation {
	t.Helper()
	return Invocation{Runtime: Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "high"}, Dir: t.TempDir(), Prompt: "Return fixture", Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}})}
}

func openCodeCapturedConfig(t *testing.T, capture string) (config, permission map[string]any) {
	t.Helper()
	if err := json.Unmarshal([]byte(recorded(t, capture, "config")), &config); err != nil {
		t.Fatalf("OPENCODE_CONFIG_CONTENT: %v", err)
	}
	permission, _ = config["permission"].(map[string]any)
	return config, permission
}

func TestOpenCodeReaderRunsRestrictedHeadlessCLI(t *testing.T) {
	t.Setenv("OPENCODE_PERMISSION", `{"bash":"allow"}`) // the parent's value must not reach a worker
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permission":{"bash":"allow"},"lsp":true,"formatter":true}`)
	t.Setenv("OPENCODE_PURE", "0")
	record, capture := openCodeFake(t, openCodeFixtureStream)
	in := openCodeInvocation(t)
	delivered, outputs, usages, activity := 0, []string{}, []map[string]any{}, []map[string]any{}
	in.Delivered = func() error { delivered++; return nil }
	in.Output = func(text string) error { outputs = append(outputs, text); return nil }
	in.Usage = func(usage map[string]any) error { usages = append(usages, usage); return nil }
	in.Diagnostic = func(kind string, data map[string]any) error {
		if kind != "provider.activity" {
			t.Fatalf("unexpected diagnostic %s", kind)
		}
		activity = append(activity, data)
		return nil
	}
	out, err := invokeOpenCode(context.Background(), in)
	if err != nil || out["summary"] != "fixture" {
		t.Fatalf("result: %v %v", out, err)
	}
	args := strings.Split(strings.TrimSuffix(recorded(t, record, "args"), "\n"), "\n")
	if want := []string{"run", "--pure", "--format", "json", "--dir", in.Dir, "--model", "opencode/cat-jam-free", "--variant", "high"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("argv: %q, want %q", args, want)
	}
	if Has(args, "--auto") {
		t.Fatal("the dangerous auto-approve flag must never be passed")
	}
	stdin := recorded(t, record, "stdin")
	for _, want := range []string{nativeWorkerInstructions, openCodeReadNote, "Return fixture", compactJSON(in.Schema)} {
		if !strings.Contains(stdin, want) {
			t.Fatalf("the prompt must arrive on stdin with %q: %s", want, stdin)
		}
	}
	// A reader gets the read tools and nothing else; Arena's map alone applies.
	config, permission := openCodeCapturedConfig(t, capture)
	for _, tool := range []string{"glob", "grep", "list"} {
		if permission[tool] != "allow" {
			t.Fatalf("%s: %v", tool, permission)
		}
	}
	read, _ := permission["read"].(map[string]any)
	if read["*"] != "allow" || read["*.env"] != "deny" || read["*.env.*"] != "deny" || read["*.env.example"] != "allow" {
		t.Fatalf("read keeps OpenCode's refusal of .env files: %v", permission["read"])
	}
	for _, tool := range []string{"*", "edit", "bash", "webfetch", "websearch", "task", "skill", "todowrite", "question", "lsp", "external_directory", "doom_loop"} {
		if permission[tool] != "deny" {
			t.Fatalf("%s must be denied: %v", tool, permission)
		}
	}
	// A worktree's own config must not loosen the map: its agent entries are
	// ignored with the rest of its config, and the build agent repeats the map.
	build, _ := config["agent"].(map[string]any)["build"].(map[string]any)
	if config["default_agent"] != "build" || !reflect.DeepEqual(build["permission"], permission) {
		t.Fatalf("build agent: %v", config)
	}
	// The parent's config above turns lsp and formatter on, as a user's global
	// opencode.json could: Arena's config turns both off, so no language server or
	// formatter runs the worktree's code outside the permission map.
	requireOpenCodeNoLSPOrFormatter(t, "reader", config)
	for file, want := range map[string]string{"permission": "unset", "projectconfig": "1", "autoupdate": "1", "pure": "1", "lspdownload": "1"} {
		if got := recorded(t, capture, file); got != want {
			t.Fatalf("%s: %q, want %q", file, got, want)
		}
	}
	cwd, _ := filepath.EvalSymlinks(strings.TrimSpace(recorded(t, capture, "cwd")))
	if want, _ := filepath.EvalSymlinks(in.Dir); cwd != want {
		t.Fatalf("working directory %q, want %q", cwd, want)
	}
	if delivered != 1 || len(outputs) != 1 || outputs[0] != `{"summary":"fixture"}` {
		t.Fatalf("delivered %d outputs %v", delivered, outputs)
	}
	if len(activity) != 2 || activity[0]["tool"] != "read" || activity[0]["status"] != "completed" || activity[0]["itemId"] != "call_1" || activity[1]["tool"] != "grep" || activity[1]["status"] != "failed" {
		t.Fatalf("activity: %v", activity)
	}
	// One usage report per model call, each marked as its own turn so the
	// roll-up adds the steps instead of keeping the largest.
	if len(usages) != 2 || usages[0]["input"] != float64(10) || usages[0]["output"] != float64(4) || usages[0][turnKey] != 1 || usages[1]["input"] != float64(20) || usages[1]["output"] != float64(6) || usages[1][turnKey] != 2 {
		t.Fatalf("usage: %v", usages)
	}
	if _, nested := usages[0]["cache"].(map[string]any); !nested {
		t.Fatalf("the provider's whole token map is kept: %v", usages[0])
	}
}

func TestOpenCodeWriterGetsEditAndOmitsVariantWithoutEffort(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"lsp":{"gopls":{"command":["gopls"]}},"formatter":true}`)
	t.Setenv("OPENCODE_DISABLE_LSP_DOWNLOAD", "0")
	record, capture := openCodeFake(t, openCodeFixtureStream)
	in := openCodeInvocation(t)
	in.Write, in.Runtime.ReasoningEffort = true, ""
	if _, err := invokeOpenCode(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if args := recorded(t, record, "args"); strings.Contains(args, "--variant") {
		t.Fatalf("no effort, no --variant: %q", args)
	}
	config, permission := openCodeCapturedConfig(t, capture)
	if permission["bash"] != "deny" || permission["*"] != "deny" || permission["external_directory"] != "deny" {
		t.Fatalf("writer permission: %v", permission)
	}
	// A writer's edits would otherwise make OpenCode start a language server and run
	// a formatter on the changed file (a user-level lsp or formatter setting applies
	// unless Arena's config sets the keys), so both stay off for a writer too.
	requireOpenCodeNoLSPOrFormatter(t, "writer", config)
	if got := recorded(t, capture, "lspdownload"); got != "1" {
		t.Fatalf("OPENCODE_DISABLE_LSP_DOWNLOAD = %q, want 1", got)
	}
	build, _ := config["agent"].(map[string]any)["build"].(map[string]any)
	if !reflect.DeepEqual(build["permission"], permission) {
		t.Fatalf("build agent: %v", config)
	}
	// A writer edits inside its worktree only. OpenCode asks the edit permission
	// with the path relative to the worktree, so a path outside it starts with
	// "../", and the last matching rule wins: the deny must come after the allow,
	// in the top-level map and in agent.build.permission alike.
	want := map[string]any{"*": "allow", "../*": "deny"}
	buildPermission, _ := build["permission"].(map[string]any)
	if !reflect.DeepEqual(permission["edit"], want) || !reflect.DeepEqual(buildPermission["edit"], want) {
		t.Fatalf("writer edit rule: top-level %v, agent.build %v, want %v", permission["edit"], buildPermission["edit"], want)
	}
	if raw := recorded(t, capture, "config"); strings.Count(raw, `"edit":{"*":"allow","../*":"deny"}`) != 2 {
		t.Fatalf("OpenCode applies rules in key order, so the outside-worktree deny must follow the allow in both places: %s", raw)
	}
	if stdin := recorded(t, record, "stdin"); !strings.Contains(stdin, openCodeWriteNote) || strings.Contains(stdin, openCodeReadNote) {
		t.Fatalf("a writer is told it may edit: %s", stdin)
	}
}

func TestOpenCodeRefusesCommandsAndUnsafeRuntimes(t *testing.T) {
	record, capture := openCodeFake(t, openCodeFixtureStream)
	in := openCodeInvocation(t)
	in.Commands = true
	if _, err := invokeOpenCode(context.Background(), in); err == nil || !strings.Contains(err.Error(), "no command sandbox") {
		t.Fatalf("commands must be refused: %v", err)
	}
	in.Commands, in.Runtime.ReasoningEffort = false, "--auto"
	if _, err := invokeOpenCode(context.Background(), in); err == nil || !strings.Contains(err.Error(), "effort") {
		t.Fatalf("a flag as effort must be refused: %v", err)
	}
	in.Runtime.ReasoningEffort, in.Runtime.Options = "", map[string]any{"authMethod": "x"}
	if _, err := invokeOpenCode(context.Background(), in); err == nil {
		t.Fatal("options must be refused")
	}
	openCodeNoCall(t, capture)
	if _, err := os.Stat(filepath.Join(record, "args")); err == nil {
		t.Fatal("opencode must not be started")
	}
}

func TestOpenCodeErrorEventsFailTheWorker(t *testing.T) {
	for name, c := range map[string]struct{ event, want, hidden string }{
		"message": {`{"type":"error","timestamp":1,"sessionID":"s","error":{"name":"UnknownError","data":{"message":"model refused the request"}}}`, "model refused the request", ""},
		"name":    {`{"type":"error","timestamp":1,"sessionID":"s","error":{"name":"ProviderAuthError"}}`, "ProviderAuthError", ""},
		"bare":    {`{"type":"error","timestamp":1,"sessionID":"s"}`, "unknown error", ""},
		"secret":  {`{"type":"error","timestamp":1,"sessionID":"s","error":{"name":"UnknownError","data":{"message":"bad key sk-abcdefghijklmnopqrstuvwxyz123456"}}}`, "[REDACTED]", "sk-abcdefghijklmnopqrstuvwxyz"},
	} {
		openCodeFake(t, "cat <<'EOF'\n"+c.event+"\nEOF\nexit 1\n")
		_, err := invokeOpenCode(context.Background(), openCodeInvocation(t))
		if err == nil || !strings.HasPrefix(err.Error(), "OpenCode run failed: ") || !strings.Contains(err.Error(), c.want) || (c.hidden != "" && strings.Contains(err.Error(), c.hidden)) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestOpenCodeNonZeroExitReportsStderrTail(t *testing.T) {
	openCodeFake(t, "echo 'Error: provider exploded' >&2\nexit 3\n")
	_, err := invokeOpenCode(context.Background(), openCodeInvocation(t))
	if err == nil || !strings.Contains(err.Error(), "OpenCode run failed: exited unsuccessfully; Error: provider exploded") {
		t.Fatalf("exit failure: %v", err)
	}
	// A long stderr is cut to its end, and secrets in it are redacted.
	openCodeFake(t, "head -c 5000 /dev/zero | tr '\\0' 'x' >&2\necho ' bearer abcdef0123456789abcdef' >&2\necho 'last line' >&2\nexit 1\n")
	_, err = invokeOpenCode(context.Background(), openCodeInvocation(t))
	if err == nil || !strings.HasSuffix(err.Error(), "last line") || strings.Contains(err.Error(), "abcdef0123456789") || len(err.Error()) > 450 {
		t.Fatalf("stderr tail: %v", err)
	}
}

// openCodeReplyScript is a fake opencode body that streams one finished text part.
func openCodeReplyScript(text string) string {
	return "cat <<'EOF'\n" + compactJSON(map[string]any{"type": "text", "part": map[string]any{"type": "text", "text": text, "time": map[string]any{"start": 1, "end": 2}}}) + "\nEOF\n"
}

func TestOpenCodeRunWithoutReplyOrWithInvalidResultFails(t *testing.T) {
	// Steps and tools but no text part.
	openCodeFake(t, "cat <<'EOF'\n{\"type\":\"step_start\",\"part\":{\"type\":\"step-start\"}}\n{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"tokens\":{\"input\":1,\"output\":1}}}\nEOF\n")
	if _, err := invokeOpenCode(context.Background(), openCodeInvocation(t)); err == nil || !strings.Contains(err.Error(), "ended without a reply") {
		t.Fatalf("no text: %v", err)
	}
	// Text that is not the requested JSON object.
	for _, reply := range []string{"I looked at the files and they are fine.", "[1,2]", `{"summary": `, "```json\n```"} {
		openCodeFake(t, openCodeReplyScript(reply))
		if _, err := invokeOpenCode(context.Background(), openCodeInvocation(t)); err == nil || !strings.Contains(err.Error(), "invalid structured output") {
			t.Fatalf("reply %q: %v", reply, err)
		}
	}
	// Synthetic and ignored parts are not the model's reply, and the last real
	// text wins.
	synthetic := `{"type":"text","part":{"type":"text","text":"{\"summary\":\"injected\"}","synthetic":true,"time":{"start":1,"end":2}}}`
	ignored := `{"type":"text","part":{"type":"text","text":"{\"summary\":\"ignored\"}","ignored":true,"time":{"start":1,"end":2}}}`
	first := `{"type":"text","part":{"type":"text","text":"{\"summary\":\"first\"}","time":{"start":1,"end":2}}}`
	last := `{"type":"text","part":{"type":"text","text":"{\"summary\":\"last\"}","time":{"start":3,"end":4}}}`
	blank := `{"type":"text","part":{"type":"text","text":"  ","time":{"start":5,"end":6}}}`
	openCodeFake(t, "cat <<'EOF'\n"+strings.Join([]string{first, last, synthetic, ignored, blank}, "\n")+"\nEOF\n")
	if out, err := invokeOpenCode(context.Background(), openCodeInvocation(t)); err != nil || out["summary"] != "last" {
		t.Fatalf("last real text: %v %v", out, err)
	}
	// The part type's hyphenated spelling is accepted as an event type too.
	openCodeFake(t, "cat <<'EOF'\n"+first+"\n{\"type\":\"step-finish\",\"part\":{\"type\":\"step-finish\",\"tokens\":{\"input\":3,\"output\":2}}}\nEOF\n")
	usages := 0
	in := openCodeInvocation(t)
	in.Usage = func(usage map[string]any) error { usages++; return nil }
	if out, err := invokeOpenCode(context.Background(), in); err != nil || out["summary"] != "first" || usages != 1 {
		t.Fatalf("hyphenated event: %v %v %d", out, err, usages)
	}
}

func TestOpenCodeResultFindsTheObjectThatEndsTheReply(t *testing.T) {
	for reply, want := range map[string]string{
		`{"summary":"a"}`:                                        "a",
		"```json\n{\"summary\":\"b\"}\n```":                      "b",
		"Here is the result:\n{\"summary\":\"c\"}":               "c",
		"Edited {x} and {y}.\n```json\n{\"summary\":\"d\"}\n```": "d",
		`Result {"summary":"e"}`:                                 "e",
	} {
		out, err := openCodeResult(reply)
		if err != nil || out["summary"] != want {
			t.Fatalf("%q -> %v %v", reply, out, err)
		}
	}
	for _, reply := range []string{"", "no object here", `{"summary":"x"} and then more prose`, `{"a": }`, "[1]", strings.Repeat("{", 200) + "x"} {
		if out, err := openCodeResult(reply); err == nil {
			t.Fatalf("%q -> %v", reply, out)
		}
	}
}

// OpenCode has no structured-output mode, so the adapter holds the reply to the
// top level of the node's schema: the required keys and additionalProperties:false.
func TestOpenCodeReplyMustMatchTheOutputSchema(t *testing.T) {
	for name, c := range map[string]struct{ reply, want, hidden string }{
		"object missing a required key": {`{"anything":1}`, `required key "summary" is missing`, ""},
		"empty object":                  {`{}`, `required key "summary" is missing`, ""},
		"extra key":                     {`{"summary":"ok","extra":1}`, `key "extra" is not in the output schema`, ""},
		"secret-shaped extra key":       {`{"summary":"ok","sk-abcdefghijklmnopqrstuvwxyz123456":1}`, "is not in the output schema", "sk-abcdefghijklmnopqrstuvwxyz"},
	} {
		openCodeFake(t, openCodeReplyScript(c.reply))
		_, err := invokeOpenCode(context.Background(), openCodeInvocation(t))
		if err == nil || !strings.Contains(err.Error(), "invalid structured output") || !strings.Contains(err.Error(), c.want) || (c.hidden != "" && strings.Contains(err.Error(), c.hidden)) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A reply that has every required key passes, and so does any reply when the
	// invocation sets no schema.
	openCodeFake(t, openCodeReplyScript(`{"summary":"ok"}`))
	if out, err := invokeOpenCode(context.Background(), openCodeInvocation(t)); err != nil || out["summary"] != "ok" {
		t.Fatalf("valid reply: %v %v", out, err)
	}
	openCodeFake(t, openCodeReplyScript(`{"anything":1}`))
	in := openCodeInvocation(t)
	in.Schema = nil
	if out, err := invokeOpenCode(context.Background(), in); err != nil || out["anything"] == nil {
		t.Fatalf("no schema: %v %v", out, err)
	}
}

func TestOpenCodeCheckSchemaCoversTheEnvelopeSchemas(t *testing.T) {
	full := func(schema map[string]any) map[string]any {
		out := map[string]any{}
		for key := range schema["properties"].(map[string]any) {
			out[key] = nil
		}
		return out
	}
	for _, role := range []string{"research", "review"} {
		schema := resultSchema(role)
		if err := openCodeCheckSchema(schema, full(schema)); err != nil {
			t.Fatalf("%s: a reply with every key must pass: %v", role, err)
		}
		for _, key := range sortedKeys(schema["properties"].(map[string]any)) {
			out := full(schema)
			delete(out, key)
			if err := openCodeCheckSchema(schema, out); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", key)) {
				t.Fatalf("%s: dropping %q must fail: %v", role, key, err)
			}
		}
		out := full(schema)
		out["notes"] = "x"
		if err := openCodeCheckSchema(schema, out); err == nil || !strings.Contains(err.Error(), `"notes"`) {
			t.Fatalf("%s: an extra key must fail: %v", role, err)
		}
	}
	// Extra keys are fine when the schema does not close the object; a schema that
	// lists no required keys asks for none.
	open := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{}}, "required": []any{"a"}}
	if err := openCodeCheckSchema(open, map[string]any{"a": 1, "b": 2}); err != nil {
		t.Fatalf("an open object takes extra keys: %v", err)
	}
	if err := openCodeCheckSchema(open, map[string]any{"b": 2}); err == nil {
		t.Fatal("a required key set as []any must still be required")
	}
	if err := openCodeCheckSchema(map[string]any{"type": "object"}, map[string]any{}); err != nil {
		t.Fatalf("no required keys: %v", err)
	}
	if err := openCodeCheckSchema(map[string]any{"required": "summary"}, map[string]any{}); err == nil {
		t.Fatal("an unreadable schema fails closed")
	}
}

func TestOpenCodeWorkerStopsWhenCancelled(t *testing.T) {
	openCodeFake(t, "sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel)
	started := time.Now()
	_, err := invokeOpenCode(ctx, openCodeInvocation(t))
	if !errors.Is(err, context.Canceled) || time.Since(started) > 10*time.Second {
		t.Fatalf("cancelled run: %v after %s", err, time.Since(started))
	}
}

func openCodeRecordedRules(t *testing.T, name string) []openCodeRule {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parseOpenCodeRules(string(b))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rules
}

func TestOpenCodeParsesRecordedBuildAgent(t *testing.T) {
	// Arena's own map resolves to 59 rules for a reader and 61 for a writer, and the
	// user's widening adds two keys (codesearch and mymcp_*), as recorded on 1.19.3.
	for name, want := range map[string]int{openCodeAgentReader: 59, openCodeAgentWriter: 61, openCodeAgentReaderWidened: 61, openCodeAgentWriterWidened: 63, openCodeAgentReaderFlippedRead: 59, openCodeAgentWriterFlippedEdit: 61} {
		rules := openCodeRecordedRules(t, name)
		if len(rules) != want {
			t.Fatalf("%s: %d rules, want %d", name, len(rules), want)
		}
		// OpenCode appends the tool-output allow after everything else.
		if last := rules[len(rules)-1]; last != (openCodeRule{"external_directory", openCodeFixtureToolOutput, "allow"}) {
			t.Fatalf("%s: last rule %+v", name, last)
		}
	}
	widened := openCodeRecordedRules(t, openCodeAgentReaderWidened)
	if widened[36] != (openCodeRule{"*", "*", "deny"}) || widened[37] != (openCodeRule{"codesearch", "*", "allow"}) || widened[38] != (openCodeRule{"mymcp_*", "*", "allow"}) {
		t.Fatalf("the user's keys land after Arena's *: %+v", widened[36:39])
	}

	// Lines before the object are skipped, and so is anything after it.
	body := `{"name":"build","permission":[{"permission":"read","pattern":"*","action":"allow"}]}`
	for _, out := range []string{body, "\n  " + body + "\n", "notice\n" + body, "a\nb\n" + body + "\ntrailing"} {
		if rules, err := parseOpenCodeRules(out); err != nil || len(rules) != 1 || rules[0].Permission != "read" {
			t.Fatalf("%q -> %v %v", out, rules, err)
		}
	}
	for name, out := range map[string]string{
		"empty":                "",
		"plain text":           "Agent build not found",
		"brace in a notice":    "notice {x}\n",
		"truncated":            `{"name":"build","permission":[{"permission":"read"`,
		"array":                `[{"permission":"read","pattern":"*","action":"allow"}]`,
		"no permission key":    `{"name":"build"}`,
		"no rules":             `{"name":"build","permission":[]}`,
		"permission an object": `{"permission":{"read":"allow"}}`,
		"unknown action":       `{"permission":[{"permission":"read","pattern":"*","action":"maybe"}]}`,
		"no action":            `{"permission":[{"permission":"read","pattern":"*"}]}`,
		"no pattern":           `{"permission":[{"permission":"read","action":"allow"}]}`,
		"no permission":        `{"permission":[{"pattern":"*","action":"allow"}]}`,
	} {
		if rules, err := parseOpenCodeRules(out); !errors.Is(err, errOpenCodeRules) || rules != nil {
			t.Fatalf("%s must be unreadable: %v %v", name, rules, err)
		}
	}
}

func TestOpenCodeGlobMatchesPermissionNames(t *testing.T) {
	for _, c := range []struct {
		name, pattern string
		want          bool
	}{
		{"codesearch", "*", true},
		{"codesearch", "codesearch", true},
		{"codesearch", "code*", true},
		{"codesearch", "*search", true},
		{"codesearch", "c?desearch", true},
		{"codesearch", "bash", false},
		{"codesearch", "code", false},
		{"codesearch", "", false},
		{"", "*", true},
		{"", "", true},
		{"mymcp_*", "*", true},
		{"mymcp_*", "mymcp_*", true},
		{"mymcp_*", "mymcp_foo", false}, // a deny for one MCP tool leaves the others
		{"mymcp_foo", "mymcp_*", true},
		{"mymcp_foo", "**", true},
		{"a/b", "a*b", true},
		{"ab", "a*b*", true},
		{"ab", "*a*b*c", false},
		// A "?" in the later permission is exactly one character, so it does not cover
		// a "*" in the allowed one: OpenCode's matcher reads "mymcp_?" as mymcp_.
		// and the name mymcp_search matches "mymcp_*" and not "mymcp_?".
		{"mymcp_*", "mymcp_?", false},
		{"a*", "a?*", false},
		{"a*", "a*?", false},
		{"*", "?", false},
		{"*", "?*", false},
		{"*", "??", false},
		{"mymcp_*", "?ymcp_*", true},
		// "?" still covers ordinary characters and another "?", and "*" covers any "?".
		{"a*", "?*", true},
		{"mymcp_foo", "mymcp_?oo", true},
		{"mymcp_?", "mymcp_?", true},
		{"mymcp_?", "mymcp_*", true},
		{"a?", "??", true},
		{"a?", "a", false},
	} {
		if got := openCodeGlob(c.name, c.pattern); got != c.want {
			t.Fatalf("openCodeGlob(%q, %q) = %v, want %v", c.name, c.pattern, got, c.want)
		}
	}
}

func TestOpenCodeToolOutputGlobFollowsXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data/xdg/")
	if got := openCodeToolOutputGlob(); got != "/data/xdg/opencode/tool-output/*" {
		t.Fatalf("XDG_DATA_HOME: %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/arena")
	if got := openCodeToolOutputGlob(); got != "/home/arena/.local/share/opencode/tool-output/*" {
		t.Fatalf("HOME fallback: %q", got)
	}
}

// The check works on the merged rules OpenCode evaluates, last match first. These
// are the recordings from 1.19.3 plus the shapes a user's configuration can take.
func TestOpenCodeUnexpectedToolsOnRecordedBuildAgents(t *testing.T) {
	for _, c := range []struct {
		name  string
		write bool
		want  []string
	}{
		// Arena's own map: every other allow is overridden by a later deny, and the
		// tool-output rule is the documented exemption.
		{openCodeAgentReader, false, []string{}},
		{openCodeAgentWriter, true, []string{}},
		// A reader that was given the writer's rules would have edit.
		{openCodeAgentWriter, false, []string{"edit"}},
		// The finding: the user's keys after "*" keep their allow.
		{openCodeAgentReaderWidened, false, []string{"codesearch", "mymcp_*"}},
		{openCodeAgentWriterWidened, true, []string{"codesearch", "mymcp_*"}},
	} {
		got := openCodeUnexpectedTools(openCodeRecordedRules(t, c.name), c.write, openCodeFixtureToolOutput)
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s (writer %v): %v, want %v", c.name, c.write, got, c.want)
		}
	}
	// The exemption is that one rule and nothing else: with another data directory,
	// or none, it is a grant like any other.
	rules := openCodeRecordedRules(t, openCodeAgentReader)
	for _, toolOutput := range []string{"", "/elsewhere/opencode/tool-output/*"} {
		if got := openCodeUnexpectedTools(rules, false, toolOutput); !reflect.DeepEqual(got, []string{"external_directory"}) {
			t.Fatalf("exemption %q: %v", toolOutput, got)
		}
	}
}

func TestOpenCodeUnexpectedToolsRules(t *testing.T) {
	allow := func(permission string) openCodeRule { return openCodeRule{permission, "*", "allow"} }
	deny := func(permission string) openCodeRule { return openCodeRule{permission, "*", "deny"} }
	ask := func(permission string) openCodeRule { return openCodeRule{permission, "*", "ask"} }
	at := func(rule openCodeRule, pattern string) openCodeRule { rule.Pattern = pattern; return rule }
	for name, c := range map[string]struct {
		rules []openCodeRule
		write bool
		want  []string
	}{
		"nothing allowed":                       {[]openCodeRule{deny("*"), ask("bash")}, false, []string{}},
		"the granted tools":                     {[]openCodeRule{deny("*"), allow("read"), allow("glob"), allow("grep"), allow("list")}, false, []string{}},
		"edit for a writer":                     {[]openCodeRule{deny("*"), allow("edit")}, true, []string{}},
		"edit for a reader":                     {[]openCodeRule{deny("*"), allow("edit")}, false, []string{"edit"}},
		"an allow nothing overrides":            {[]openCodeRule{deny("*"), allow("codesearch")}, false, []string{"codesearch"}},
		"an allow before the catch-all":         {[]openCodeRule{allow("codesearch"), deny("*")}, false, []string{}},
		"an allow before a named deny":          {[]openCodeRule{allow("codesearch"), deny("codesearch")}, false, []string{}},
		"an allow before an ask":                {[]openCodeRule{allow("codesearch"), ask("codesearch")}, false, []string{}},
		"an allow before a wildcard":            {[]openCodeRule{allow("codesearch"), deny("code*")}, false, []string{}},
		"an allow before a deny of other tools": {[]openCodeRule{allow("codesearch"), deny("bash"), deny("webfetch")}, false, []string{"codesearch"}},
		"an allow before a narrow deny":         {[]openCodeRule{allow("codesearch"), at(deny("codesearch"), "secret*")}, false, []string{"codesearch"}},
		"an allow again after a deny":           {[]openCodeRule{allow("codesearch"), deny("codesearch"), allow("codesearch")}, false, []string{"codesearch"}},
		"a narrow allow":                        {[]openCodeRule{deny("*"), at(allow("bash"), "git status")}, false, []string{"bash"}},
		"a narrow allow, later bare deny":       {[]openCodeRule{at(allow("bash"), "git status"), deny("bash")}, false, []string{}},
		"a wildcard allow":                      {[]openCodeRule{deny("*"), allow("mymcp_*")}, false, []string{"mymcp_*"}},
		"a wildcard allow, one MCP tool denied": {[]openCodeRule{deny("*"), allow("mymcp_*"), deny("mymcp_foo")}, false, []string{"mymcp_*"}},
		"a wildcard allow, wildcard denied":     {[]openCodeRule{deny("*"), allow("mymcp_*"), deny("mymcp_*")}, false, []string{}},
		"the catch-all allowed last":            {[]openCodeRule{deny("bash"), allow("*")}, false, []string{"*"}},
		// A "?" stands for one character, so a later deny of "mymcp_?" leaves the
		// MCP tools whose names are longer allowed by "mymcp_*" (OpenCode's matcher
		// reads "?" as "." and "*" as ".*", so mymcp_search matches only the allow).
		"a wildcard allow, one-character deny":  {[]openCodeRule{deny("*"), allow("mymcp_*"), deny("mymcp_?")}, false, []string{"mymcp_*"}},
		"a wildcard allow, ask for one char":    {[]openCodeRule{deny("*"), allow("mymcp_*"), ask("mymcp_?")}, false, []string{"mymcp_*"}},
		"a star allow, later question-star":     {[]openCodeRule{allow("a*"), deny("a?*")}, false, []string{"a*"}},
		"the catch-all allowed, one-char deny":  {[]openCodeRule{allow("*"), deny("?")}, false, []string{"*"}},
		"a one-char allow, same pattern denied": {[]openCodeRule{allow("mymcp_?"), deny("mymcp_?")}, false, []string{}},
		"a one-char allow, wildcard denied":     {[]openCodeRule{allow("mymcp_?"), deny("mymcp_*")}, false, []string{}},
		"a wildcard allow, ?-prefixed wildcard": {[]openCodeRule{allow("mymcp_*"), deny("?ymcp_*")}, false, []string{}},
		"a double star deny covers":             {[]openCodeRule{allow("codesearch"), at(deny("*"), "**")}, false, []string{}},
		"an ask that is not for every pattern":  {[]openCodeRule{allow("codesearch"), at(ask("codesearch"), "q*")}, false, []string{"codesearch"}},
		"another external directory":            {[]openCodeRule{deny("*"), at(allow("external_directory"), "/home/arena/shared/*")}, false, []string{"external_directory"}},
		"the tool-output rule":                  {[]openCodeRule{deny("*"), at(allow("external_directory"), openCodeFixtureToolOutput)}, false, []string{}},
		"names listed once and sorted":          {[]openCodeRule{deny("*"), allow("zeta"), allow("alpha"), at(allow("zeta"), "x")}, false, []string{"alpha", "zeta"}},
	} {
		if got := openCodeUnexpectedTools(c.rules, c.write, openCodeFixtureToolOutput); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// OpenCode's Wildcard.match, as read from the 1.19.3 bundle: "*" is any run of
// characters, "/" included, "?" is any one, the match is anchored, "\\" reads as "/",
// and a pattern ending in " *" also matches without that tail.
func TestOpenCodeWildcardMatchesLikeOpenCode(t *testing.T) {
	for _, c := range []struct {
		input, pattern string
		want           bool
	}{
		{".env", "*.env", true},
		{"a/.env", "*.env", true},
		{"a/b/.env", "*.env", true},
		{"a.env", "*.env", true},
		{".envrc", "*.env", false},
		{"a/.env.local", "*.env.*", true},
		{".env.local", "*.env.*", true},
		{".env", "*.env.*", false},
		{".env.example", "*.env.example", true},
		{"a/.env.example", "*.env.example", true},
		{".env.example", "*.env.*", true},
		{"src/a.go", "*", true},
		{"", "*", true},
		{"../x", "../*", true},
		{"../../d/opencode/tool-output/tool_1", "../*", true},
		{"src/a.go", "../*", false},
		{"x/../y", "../*", false},
		{"..", "../*", false},
		{"edit", "edit", true},
		{"edit", "*", true},
		{"edit", "ed?t", true},
		{"edit", "ed?", false},
		{"edit", "edit*", true},
		{"edit", "e*t", true},
		{"edit", "bash", false},
		{"a.go", "a.go", true},
		{"abgo", "a.go", false}, // "." is a literal dot, not any character
		{"a+b", "a+b", true},
		{"a(b)", "a(b)", true},
		{"ls", "ls *", true}, // a trailing " *" is optional
		{"ls -la", "ls *", true},
		{"lsx", "ls *", false},
		{"a\\b", "a/b", true},
		{"a/b", "a\\b", true},
		{"a\nb", "a*b", true}, // matches newlines
	} {
		if got := openCodeWildcard(c.input, c.pattern); got != c.want {
			t.Fatalf("openCodeWildcard(%q, %q) = %v, want %v", c.input, c.pattern, got, c.want)
		}
	}
}

// Permission.evaluate takes the last rule whose permission and pattern both match,
// with wildcards on both sides, and asks when none does.
func TestOpenCodeDecideTakesTheLastMatch(t *testing.T) {
	rules := []openCodeRule{
		{"*", "*", "allow"},
		{"edit", "*", "deny"},
		{"edit", "src/*", "allow"},
		{"e*", "src/gen/*", "ask"},
		{"read", "*", "allow"},
	}
	for _, c := range []struct {
		permission, pattern, action string
		index                       int
	}{
		{"edit", "README.md", "deny", 1},
		{"edit", "src/a.go", "allow", 2},
		{"edit", "src/gen/a.go", "ask", 3}, // a wildcard permission matches the name
		{"read", "src/gen/a.go", "allow", 4},
		{"bash", "ls", "allow", 0},
	} {
		if action, index := openCodeDecide(rules, c.permission, c.pattern); action != c.action || index != c.index {
			t.Fatalf("%s %q: %s at rule %d, want %s at rule %d", c.permission, c.pattern, action, index, c.action, c.index)
		}
	}
	if action, index := openCodeDecide(nil, "edit", "a"); action != "ask" || index != -1 {
		t.Fatalf("no rule asks: %s %d", action, index)
	}
	if action, index := openCodeDecide([]openCodeRule{{"read", "*", "allow"}}, "edit", "a"); action != "ask" || index != -1 {
		t.Fatalf("no matching rule asks: %s %d", action, index)
	}
}

// The recorded rule lists of Arena's own map give every guarantee; the two flipped
// recordings (a user's agent.build.permission edit or read object listed before
// Arena's keys, so OpenCode puts Arena's allow after its deny) do not. Those are
// the shapes openCodeUnexpectedTools cannot see, because it skips every read rule
// and every edit rule of a writer: it reports nothing for either.
func TestOpenCodeFlippedRulesOnRecordedBuildAgents(t *testing.T) {
	for _, c := range []struct {
		name    string
		write   bool
		flipped []string
	}{
		{openCodeAgentReader, false, nil},
		{openCodeAgentWriter, true, nil},
		{openCodeAgentReaderWidened, false, nil},
		{openCodeAgentWriterWidened, true, nil},
		// edit * allow (rule 39) comes after edit ../* deny (rule 38): a writer may edit
		// outside the worktree, tool-output included.
		{openCodeAgentWriterFlippedEdit, true, []string{
			`edit "../x" resolves to allow, not deny, by the rule edit * allow`,
			`edit "../../d/opencode/tool-output/tool_1" resolves to allow, not deny, by the rule edit * allow`,
		}},
		// read * allow (rule 38) comes after read *.env deny (rule 37): .env is readable.
		{openCodeAgentReaderFlippedRead, false, []string{
			`read ".env" resolves to allow, not deny, by the rule read * allow`,
			`read "a/.env" resolves to allow, not deny, by the rule read * allow`,
		}},
	} {
		rules := openCodeRecordedRules(t, c.name)
		got := openCodeFlippedRules(rules, c.write)
		if len(c.flipped) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.flipped) {
			t.Fatalf("%s (writer %v): %q, want %q", c.name, c.write, got, c.flipped)
		}
		if tools := openCodeUnexpectedTools(rules, c.write, openCodeFixtureToolOutput); len(tools) != 0 {
			t.Fatalf("%s: the tool check was expected to miss these, it reports %v", c.name, tools)
		}
	}
	// The edit probes belong to the writer's run: the reader's edit is the plain
	// string deny and a reader that gets edit is the tool check's finding.
	if got := openCodeFlippedRules(openCodeRecordedRules(t, openCodeAgentWriterFlippedEdit), false); len(got) != 0 {
		t.Fatalf("a reader is not held to the edit probes: %q", got)
	}
}

func TestOpenCodeFlippedRulesProbes(t *testing.T) {
	deny := openCodeRule{"*", "*", "deny"}
	// Arena's patterns in the right order, as OpenCode lists them.
	arenaRead := []openCodeRule{{"read", "*", "allow"}, {"read", "*.env", "deny"}, {"read", "*.env.*", "deny"}, {"read", "*.env.example", "allow"}}
	arenaEdit := []openCodeRule{{"edit", "*", "allow"}, {"edit", "../*", "deny"}}
	list := func(parts ...[]openCodeRule) []openCodeRule {
		rules := []openCodeRule{deny}
		for _, part := range parts {
			rules = append(rules, part...)
		}
		return rules
	}
	for name, c := range map[string]struct {
		rules []openCodeRule
		write bool
		want  []string
	}{
		"Arena's order, reader": {list(arenaRead), false, nil},
		"Arena's order, writer": {list(arenaRead, arenaEdit), true, nil},
		// (a) the deny for ../* comes first, the allow for * last.
		"writer, edit allow after the outside deny": {list(arenaRead, []openCodeRule{{"edit", "../*", "deny"}, {"edit", "*", "allow"}}), true, []string{
			`edit "../x" resolves to allow, not deny, by the rule edit * allow`,
			`edit "../../d/opencode/tool-output/tool_1" resolves to allow, not deny, by the rule edit * allow`,
		}},
		// (b) read * allow listed after the .env denies: .env, a/.env and a/.env.local.
		"read allow after the .env denies": {list([]openCodeRule{{"read", "*.env", "deny"}, {"read", "*.env.*", "deny"}, {"read", "*", "allow"}, {"read", "*.env.example", "allow"}}), false, []string{
			`read ".env" resolves to allow, not deny, by the rule read * allow`,
			`read "a/.env" resolves to allow, not deny, by the rule read * allow`,
			`read "a/.env.local" resolves to allow, not deny, by the rule read * allow`,
		}},
		// A user ask on .env after Arena's deny is not a refusal.
		"read ask after the .env deny": {list(arenaRead, []openCodeRule{{"read", "*.env", "ask"}}), false, []string{
			`read ".env" resolves to ask, not deny, by the rule read *.env ask`,
			`read "a/.env" resolves to ask, not deny, by the rule read *.env ask`,
		}},
		// A user deny that closes ordinary files, or the example file, leaves the
		// worker unable to do its job: readiness reports that too.
		"read denied for source files": {list(arenaRead, []openCodeRule{{"read", "src/*", "deny"}}), false, []string{
			`read "src/a.go" resolves to deny, not allow, by the rule read src/* deny`,
		}},
		"read of the example file asks": {list(arenaRead, []openCodeRule{{"read", "*.env.example", "ask"}}), false, []string{
			`read ".env.example" resolves to ask, not allow, by the rule read *.env.example ask`,
		}},
		"writer cannot edit its worktree": {list(arenaRead, arenaEdit, []openCodeRule{{"edit", "src/*", "ask"}}), true, []string{
			`edit "src/a.go" resolves to ask, not allow, by the rule edit src/* ask`,
		}},
		// A wildcard permission and pattern decide too.
		"the catch-all allowed last": {list(arenaRead, arenaEdit, []openCodeRule{{"*", "*", "allow"}}), true, []string{
			`read ".env" resolves to allow, not deny, by the rule * * allow`,
			`read "a/.env" resolves to allow, not deny, by the rule * * allow`,
			`read "a/.env.local" resolves to allow, not deny, by the rule * * allow`,
			`edit "../x" resolves to allow, not deny, by the rule * * allow`,
			`edit "../../d/opencode/tool-output/tool_1" resolves to allow, not deny, by the rule * * allow`,
		}},
		"no rule at all": {nil, true, []string{
			`read ".env" resolves to ask, not deny, because no rule matches`,
			`read "a/.env" resolves to ask, not deny, because no rule matches`,
			`read "a/.env.local" resolves to ask, not deny, because no rule matches`,
			`read ".env.example" resolves to ask, not allow, because no rule matches`,
			`read "src/a.go" resolves to ask, not allow, because no rule matches`,
			`edit "src/a.go" resolves to ask, not allow, because no rule matches`,
			`edit "../x" resolves to ask, not deny, because no rule matches`,
			`edit "../../d/opencode/tool-output/tool_1" resolves to ask, not deny, because no rule matches`,
		}},
	} {
		got := openCodeFlippedRules(c.rules, c.write)
		if len(c.want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

// A long list is cut to five names, and a name is redacted and shortened like any
// other text the probe reports.
func TestOpenCodeProbeReportsWidenedToolsBriefly(t *testing.T) {
	cat := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free"}
	rules := []openCodeRule{{Permission: "*", Action: "deny"}}
	for _, name := range []string{"tool_g", "tool_f", "tool_e", "tool_d", "tool_c", "tool_b", "tool_a"} {
		rules = append(rules, openCodeRule{Permission: name, Action: "allow"})
	}
	openCodeProbeCLI{agentReader: openCodeAgentJSON(rules...)}.install(t)
	got := probeOpenCode(context.Background(), cat, openCodeReadiness(cat))
	if got.CanStart || got.State != "unsupported" || !strings.Contains(got.Detail, "tool_a, tool_b, tool_c, tool_d, tool_e and 2 more.") || strings.Contains(got.Detail, "tool_f") {
		t.Fatalf("readiness: %+v", got)
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz123456"
	openCodeProbeCLI{agentReader: openCodeAgentJSON(openCodeRule{Permission: "*", Action: "deny"}, openCodeRule{Permission: secret, Action: "allow"})}.install(t)
	got = probeOpenCode(context.Background(), cat, openCodeReadiness(cat))
	if got.CanStart || got.State != "unsupported" || strings.Contains(got.Detail, secret) || !strings.Contains(got.Detail, "[REDACTED]") {
		t.Fatalf("readiness: %+v", got)
	}
}

// An allow that comes after Arena's "*" deny is the case the finding describes, and
// the probe must refuse it even when the model, the effort and the sign-in are fine.
func TestOpenCodeProbeRefusesAUserMapThatListsToolsAfterTheCatchAll(t *testing.T) {
	r := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "xhigh"}
	_, capture := openCodeProbeCLI{agentReader: openCodeCat(t, openCodeAgentReaderWidened), agentWriter: openCodeCat(t, openCodeAgentWriterWidened)}.install(t)
	got := probeOpenCode(context.Background(), r, openCodeReadiness(r))
	if got.CanStart || got.State != "unsupported" || got.ModelState != "unknown" || len(got.Efforts) != 0 {
		t.Fatalf("readiness: %+v", got)
	}
	for _, want := range []string{"codesearch, mymcp_*", "agent.build.permission", "OPENCODE_CONFIG", "global agent definition", "MCP tools included"} {
		if !strings.Contains(got.Detail, want) {
			t.Fatalf("the detail must say %q: %s", want, got.Detail)
		}
	}
	// It stops before the catalog is read and before any run.
	if calls := recorded(t, capture, "calls"); calls != openCodeProbeThroughAgent {
		t.Fatalf("calls: %q", calls)
	}
	// And the same setup with Arena's own recorded rules is ready.
	openCodeProbeCLI{}.install(t)
	if got := probeOpenCode(context.Background(), r, openCodeReadiness(r)); !got.CanStart {
		t.Fatalf("a clean configuration must pass: %+v", got)
	}
}
