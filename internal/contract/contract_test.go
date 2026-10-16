package contract

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryFixtures(t *testing.T) {
	root := "../../examples/project/.arena"
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var validate func([]byte) error
		switch {
		case strings.HasSuffix(path, "settings.arena.json"):
			validate = ValidateProjectSettings
		case strings.HasSuffix(path, ".arena.json"):
			validate = ValidateWorkflow
		case strings.HasSuffix(path, ".agent.json"):
			validate = ValidateProfile
		case strings.HasSuffix(path, ".context.json"):
			validate = ValidateContextEntry
		default:
			return nil
		}
		if err := validate(b); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestJSONNumbersRemainExactAndBoundedWithoutFloatConversion(t *testing.T) {
	for _, pair := range [][3]string{{"9007199254740992", "9007199254740993", "-1"}, {"1.00", "1e0", "0"}, {"-1e-999999999", "0", "-1"}, {"1e999999999", "1e999999998", "1"}, {"-0", "0", "0"}} {
		want := map[string]int{"-1": -1, "0": 0, "1": 1}[pair[2]]
		if got := compareNumbers(json.Number(pair[0]), json.Number(pair[1])); got != want {
			t.Fatalf("compare %s/%s: %d", pair[0], pair[1], got)
		}
	}
	for _, n := range []string{"1.0", "1e999999999", "0.00"} {
		if !hasType(json.Number(n), "integer") {
			t.Fatalf("integer rejected: %s", n)
		}
	}
	if hasType(json.Number("0.01"), "integer") {
		t.Fatal("fraction accepted as integer")
	}
	if _, err := decode([]byte(`{"opaque":1e999999999,"seed":9007199254740993}`)); err != nil {
		t.Fatal(err)
	}
}
func TestGeneratedSourceIsReproducible(t *testing.T) {
	before, e := os.ReadFile("generated.go")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("go", "run", "../../cmd/schemagen", "../..")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generator: %v: %s", e, b)
	}
	after, e := os.ReadFile("generated.go")
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("generated contract source is stale; run go generate ./internal/contract")
	}
}
func TestValidationRejectsAmbiguityAndWrongVersions(t *testing.T) {
	for _, b := range []string{`{"schemaVersion":1,"schemaVersion":2}`, `{"schemaVersion":2}`, `{"schemaVersion":1} {}`} {
		if ValidateWorkflow([]byte(b)) == nil {
			t.Fatalf("accepted %s", b)
		}
	}
}
func TestFanOutResultNeedsProtocolOneThree(t *testing.T) {
	envelope := func(version string, payload string) []byte {
		return []byte(`{"protocolVersion":"` + version + `","messageId":"message-1","runId":"run-1","kind":"fanout-result","from":{"agentId":"split","provider":"arena"},"to":{"agentId":"lead","provider":"codex"},"createdAt":"2026-10-16T00:00:00Z","correlationId":"c","attemptId":"a","replyTo":null,"payload":` + payload + `,"providerContext":{"model":"","reasoningEffort":"","options":{}}}`)
	}
	payload := `{"format":"arena.fanout-result.v1","fanOutId":"split","summary":"1 of 1 children succeeded.","planned":1,"results":[{"childId":"split.1","item":{"title":"t","instructions":"i"},"status":"succeeded","messageId":"message-2","changeIds":[],"summary":"done"}],"dropped":[]}`
	if err := ValidateEnvelope(envelope("1.3", payload)); err != nil {
		t.Fatal(err)
	}
	if ValidateEnvelope(envelope("1.2", payload)) == nil {
		t.Fatal("fanout-result accepted below protocol 1.3")
	}
	if err := ValidateEnvelope(envelope("1.10", payload)); err != nil {
		t.Fatalf("protocol 1.10: %v", err)
	}
	for _, bad := range []string{
		`{"format":"arena.fanout-result.v1","fanOutId":"split","summary":"s","planned":1,"results":[{"childId":"split.1","item":{"title":"t","instructions":"i"},"status":"skipped","messageId":null,"changeIds":[],"summary":""}],"dropped":[]}`,
		`{"format":"arena.fanout-result.v1","fanOutId":"split","summary":"s","planned":0,"results":[]}`,
	} {
		if ValidateEnvelope(envelope("1.3", bad)) == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
