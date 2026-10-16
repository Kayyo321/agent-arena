package arena

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// Scripts parse `arena history` and `arena history --json`: both have always
// printed the project's run IDs as one JSON array of strings, byte for byte the
// same. Only `--all` adds the {"id", "imported"} objects. This pins all three
// shapes so a richer listing cannot leak into the compatible forms.
func TestHistoryListOutputShapesStayScriptCompatible(t *testing.T) {
	p := committedProject(t)
	history := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cli := NewCLI(strings.NewReader(""), &out, &bytes.Buffer{})
		if err := cli.Run(context.Background(), append(append([]string{"history"}, args...), "--project", p.Root)); err != nil {
			t.Fatalf("history %v: %v", args, err)
		}
		return out.String()
	}
	idsOf := func(out string) []string {
		t.Helper()
		var ids []string
		if err := json.Unmarshal([]byte(out), &ids); err != nil {
			t.Fatalf("not a JSON array of strings: %q (%v)", out, err)
		}
		return ids
	}

	if plain, asJSON := history(), history("--json"); plain != "[]\n" || asJSON != "[]\n" {
		t.Fatalf("no runs: plain %q, --json %q, want %q", plain, asJSON, "[]\n")
	}

	first := testRun(t, p)
	second, err := p.CreateRun(first.Workflow, first.Profiles, first.Settings, "second", first.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{first.ID, second.ID}
	sort.Strings(want)

	plain, asJSON := history(), history("--json")
	if plain != asJSON {
		t.Fatalf("plain and --json differ:\nplain %q\njson  %q", plain, asJSON)
	}
	if got := idsOf(plain); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("run IDs = %v, want %v", got, want)
	}
	if strings.Contains(plain, "{") || strings.Contains(plain, "imported") {
		t.Fatalf("the compatible listing carries objects: %q", plain)
	}

	var all []map[string]any
	if err := json.Unmarshal([]byte(history("--all")), &all); err != nil || len(all) != len(want) {
		t.Fatalf("--all: %v %+v", err, all)
	}
	for i, entry := range all {
		if len(entry) != 2 || entry["id"] != want[i] || entry["imported"] != false {
			t.Fatalf("--all entry %d = %+v, want exactly {id: %s, imported: false}", i, entry, want[i])
		}
	}
}
