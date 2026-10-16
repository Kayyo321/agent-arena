package arena

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// gauge tracks how many fake provider invocations are active at once, in
// total and per run, and the highest each ever reached.
type gauge struct {
	mu          sync.Mutex
	active      int
	peak        int
	perRun      map[string]int
	perRunPeak  map[string]int
	holdTime    time.Duration
	invocations int
}

func newGauge() *gauge {
	return &gauge{perRun: map[string]int{}, perRunPeak: map[string]int{}, holdTime: 150 * time.Millisecond}
}

func (g *gauge) invoke(ctx context.Context, in Invocation) (map[string]any, error) {
	g.mu.Lock()
	g.invocations++
	g.active++
	g.perRun[in.RunID]++
	g.peak = max(g.peak, g.active)
	g.perRunPeak[in.RunID] = max(g.perRunPeak[in.RunID], g.perRun[in.RunID])
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.active--
		g.perRun[in.RunID]--
		g.mu.Unlock()
	}()
	select {
	case <-time.After(g.holdTime):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return ordinaryResult(), nil
}

// fanOutRun saves a workflow whose head delegates to `workers` read-only
// agents that are all ready at once, and creates a run with the given
// per-run cap (workflow settings) under the project cap.
func fanOutRun(t *testing.T, p *Project, id string, workers, runCap int) Run {
	t.Helper()
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow(id, id)
	w.HeadAgent = "head"
	w.Settings.MaxConcurrentAgents = runCap
	profiles := map[string]Profile{}
	ids := []string{"head"}
	for i := 0; i < workers; i++ {
		ids = append(ids, fmt.Sprintf("worker-%d", i))
	}
	for i, agent := range ids {
		level := Int(1)
		if i == 0 {
			level = Int(0)
		} else {
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "head", To: agent})
		}
		w.Agents = append(w.Agents, Agent{Id: agent, Level: level, Profile: ".agents/" + id + "/" + agent + ".agent.json", Runtime: s.DefaultModel})
		profiles[agent] = NewProfile(agent, "research", "Inspect evidence", "Return the requested structured result", []string{"read"})
	}
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, id+" fixture", head)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestActiveAgentsNeverExceedTheRunSlots(t *testing.T) {
	p := committedProject(t)
	r := fanOutRun(t, p, "run-cap", 8, 2)
	if r.Settings.Limits.MaxConcurrentAgents <= 2 {
		t.Fatal("the project cap must be looser than the run cap for this test to isolate the run's slots")
	}
	g := newGauge()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := NewEngine(p, r, testInvoker(g.invoke)).Execute(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	if g.invocations != 9 {
		t.Fatalf("%d invocations", g.invocations)
	}
	if g.peak != 2 {
		t.Fatalf("peak %d active agents; the run allows exactly 2 and 8 workers were ready", g.peak)
	}
}

func TestProjectSlotsBoundAgentsAcrossRuns(t *testing.T) {
	p := committedProject(t)
	// Each run may use 3 agents, but the project only has 4 provider slots
	// (project limits.maxConcurrentAgents), shared by both runs.
	a, b := fanOutRun(t, p, "run-a", 8, 3), fanOutRun(t, p, "run-b", 8, 3)
	if a.Settings.Limits.MaxConcurrentAgents != 4 {
		t.Fatalf("project cap %d, want the default 4", a.Settings.Limits.MaxConcurrentAgents)
	}
	g := newGauge()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, r := range []Run{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- NewEngine(p, r, testInvoker(g.invoke)).Execute(ctx, false, false)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if g.invocations != 18 {
		t.Fatalf("%d invocations", g.invocations)
	}
	if g.peak != 4 {
		t.Fatalf("peak %d active agents across both runs; the project has 4 slots and 6 were wanted", g.peak)
	}
	for id, peak := range g.perRunPeak {
		if peak > 3 {
			t.Fatalf("run %s reached %d active agents; its own cap is 3", id, peak)
		}
	}
}
