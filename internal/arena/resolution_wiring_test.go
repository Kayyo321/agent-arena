package arena

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func twoWriters(t *testing.T) (*Project, Run, Change, Change) {
	t.Helper()
	p, r := resolutionRun(t)
	a := propose(t, p, r, "alpha", map[string]string{"alpha.txt": "a\n"})
	b := propose(t, p, r, "beta", map[string]string{"beta.txt": "b\n"})
	succeedRun(t, p, r)
	return p, r, a, b
}

func announceOutput(p *Project, r Run) string {
	var buf strings.Builder
	cli := NewCLI(strings.NewReader(""), &buf, &buf)
	cli.announceResolution(p, r.ID)
	return buf.String()
}

func TestAnnounceResolutionPrintsReadyLine(t *testing.T) {
	p, r, _, _ := twoWriters(t)
	out := announceOutput(p, r)
	for _, want := range []string{"Resolution ready: 2 changes", "arena integrate " + r.ID, "--all"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q missing %q", out, want)
		}
	}
}

func TestAnnounceResolutionReportsNotReadyWhenDirty(t *testing.T) {
	p, r, _, _ := twoWriters(t)
	if e := os.WriteFile(filepath.Join(p.Root, "stray.txt"), []byte("x\n"), 0644); e != nil {
		t.Fatal(e)
	}
	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil || res.Ready || res.Reason == "" {
		t.Fatalf("expected not ready with reason: %+v %v", res, e)
	}
	out := announceOutput(p, r)
	if !strings.Contains(out, "Resolution not ready") || !strings.Contains(out, res.Reason) {
		t.Fatalf("output %q lacks not-ready text or reason %q", out, res.Reason)
	}
}

func TestAnnounceResolutionSilentWithoutProposals(t *testing.T) {
	p, r := resolutionRun(t)
	succeedRun(t, p, r)
	if out := announceOutput(p, r); out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
}

func assertCleanCheckout(t *testing.T, p *Project) {
	t.Helper()
	if s := resolutionGit(t, p.Root, "status", "--porcelain"); strings.TrimSpace(s) != "" {
		t.Fatalf("checkout not clean: %q", s)
	}
}

func TestCommandIntegrateAllAppliesResolution(t *testing.T) {
	p, r, a, b := twoWriters(t)
	before := resolutionGit(t, p.Root, "rev-parse", "HEAD")
	m := LiveModel{Project: p, Run: r}
	if e := m.command("integrate all"); e != nil {
		t.Fatal(e)
	}
	for _, c := range []Change{a, b} {
		if s := changeState(t, p, r, c.ID); s != "integrated" {
			t.Fatalf("change %s is %s", c.ID, s)
		}
	}
	if after := resolutionGit(t, p.Root, "rev-parse", "HEAD"); after == before {
		t.Fatal("HEAD did not move")
	}
	assertCleanCheckout(t, p)
}

func TestCommandIntegrateAllWithSkip(t *testing.T) {
	p, r, a, b := twoWriters(t)
	before := resolutionGit(t, p.Root, "rev-parse", "HEAD")
	m := LiveModel{Project: p, Run: r}
	if e := m.command("integrate all skip " + a.ID); e != nil {
		t.Fatal(e)
	}
	if s := changeState(t, p, r, a.ID); s != "proposed" {
		t.Fatalf("skipped change is %s", s)
	}
	if s := changeState(t, p, r, b.ID); s != "integrated" {
		t.Fatalf("other change is %s", s)
	}
	if after := resolutionGit(t, p.Root, "rev-parse", "HEAD"); after == before {
		t.Fatal("HEAD did not move")
	}
	assertCleanCheckout(t, p)
}

func TestExecuteHookJournalsReadyThroughRun(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	w, profiles, version, err := p.LoadVersion(r.Workflow.Id)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles["head"]
	profile.Capabilities = []string{"read", "write"}
	profiles["head"] = profile
	if _, err := p.Save(w, profiles, version); err != nil {
		t.Fatal(err)
	}
	r, err = p.CreateRun(w, profiles, r.Settings, "Writing head", r.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := Git(p.Root, "add", ".arena"); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "workflow"); e != nil {
		t.Fatal(e)
	}
	fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if err := os.WriteFile(filepath.Join(in.Dir, "written.txt"), []byte("from head\n"), 0644); err != nil {
			return nil, err
		}
		return ordinaryResult(), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := NewEngine(p, r, fake).Execute(ctx, false, false); e != nil {
		t.Fatal(e)
	}
	events := resolutionEvents(t, p, r)
	if len(events) != 1 || events[0].Type != "resolution.ready" {
		t.Fatalf("events: %+v", events)
	}
	d := events[0].Data
	if resolutionInt(d["ready"]) != 1 {
		t.Fatalf("ready data: %+v", d)
	}
	if ok, _ := d["applicable"].(bool); !ok {
		t.Fatalf("not applicable: %+v", d)
	}
}
