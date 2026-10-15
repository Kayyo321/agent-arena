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
