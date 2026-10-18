package arena

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadyPayloadReportsApplicability(t *testing.T) {
	p, r := resolutionRun(t)
	propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	succeedRun(t, p, r)
	if e := os.WriteFile(filepath.Join(p.Root, "dirty.txt"), []byte("x\n"), 0644); e != nil {
		t.Fatal(e)
	}

	NewEngine(p, r, nil).journalResolution()
	events := resolutionEvents(t, p, r)
	if len(events) != 1 || events[0].Type != "resolution.ready" {
		t.Fatalf("events: %+v", events)
	}
	d := events[0].Data
	if app, ok := d["applicable"].(bool); !ok || app || str(d["reason"]) == "" {
		t.Fatalf("dirty checkout payload: %+v", d)
	}
	if resolutionInt(d["ready"]) != 1 {
		t.Fatalf("existing keys changed: %+v", d)
	}

	if e := os.Remove(filepath.Join(p.Root, "dirty.txt")); e != nil {
		t.Fatal(e)
	}
	NewEngine(p, r, nil).journalResolution()
	events = resolutionEvents(t, p, r)
	if len(events) != 2 {
		t.Fatalf("events: %+v", events)
	}
	d = events[1].Data
	if app, ok := d["applicable"].(bool); !ok || !app || str(d["reason"]) != "" {
		t.Fatalf("clean checkout payload: %+v", d)
	}
}

func TestStaleIntegrationBranchBlocksItem(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	b := propose(t, p, r, "b", map[string]string{"b.txt": "b\n"}, a.ID)
	succeedRun(t, p, r)
	resolutionGit(t, p.Root, "branch", "codex/arena/integrate/"+a.ID)
	head := resolutionGit(t, p.Root, "rev-parse", "HEAD")

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	if res.Ready || res.Reason == "" {
		t.Fatalf("stale branch must not be ready: %+v", res)
	}
	st := itemStatuses(res)
	if st[a.ID] != "blocked" || st[b.ID] != "blocked" {
		t.Fatalf("statuses: %+v", st)
	}
	for _, item := range res.Items {
		if item.ChangeID == a.ID && !strings.Contains(item.Reason, "stale integration branch") {
			t.Fatalf("reason: %q", item.Reason)
		}
	}
	if _, e := p.ApplyResolution(r.ID, "tester", nil); e == nil {
		t.Fatal("apply must refuse")
	}
	if changeState(t, p, r, a.ID) != "proposed" || changeState(t, p, r, b.ID) != "proposed" {
		t.Fatal("change state moved")
	}
	if got := resolutionGit(t, p.Root, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved: %s -> %s", head, got)
	}
}

func TestCancelledRunStillDrainingIsNotReady(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	startRun(t, p, r)
	if e := p.Transition(r.ID, "cancelled"); e != nil {
		t.Fatal(e)
	}
	// A cancelled run nobody owns is finished and can resolve.
	if res, e := p.PrepareResolution(r.ID, nil); e != nil || !res.Ready {
		t.Fatalf("dead cancelled run: %+v %v", res, e)
	}
	// This process holds the run lock, so RunAlive reports it still alive.
	l, e := p.Lock("run-" + r.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if !RunAlive(p, r.ID) {
		t.Fatal("RunAlive should be true while the lock is held")
	}
	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil || res.Ready || res.Reason != "run is still stopping; wait for it to finish, then run /integrate again" {
		t.Fatalf("draining run: %+v %v", res, e)
	}
	if _, e := p.ApplyResolution(r.ID, "tester", nil); e == nil {
		t.Fatal("apply must refuse")
	}
	if changeState(t, p, r, a.ID) != "proposed" {
		t.Fatal("change applied")
	}
}
