package arena

import (
	"encoding/json"
	"testing"
)

func TestLatestResolutionReadyFollowsNewestResolutionEvent(t *testing.T) {
	ready := Event{Type: "resolution.ready", Data: map[string]any{"ready": float64(2), "applicable": true}}
	notReady := Event{Type: "resolution.ready", Data: map[string]any{"ready": float64(0), "applicable": false}}
	applied := Event{Type: "resolution.applied"}
	stopped := Event{Type: "resolution.stopped"}
	cases := []struct {
		name        string
		events      []Event
		want        int
		wantApplies bool
		ok          bool
	}{
		{"none", []Event{{Type: "workflow.succeeded"}}, 0, false, false},
		{"ready", []Event{{Type: "workflow.succeeded"}, ready}, 2, true, true},
		{"not ready", []Event{notReady}, 0, false, true},
		{"applied after ready", []Event{ready, applied}, 0, false, false},
		{"stopped after ready", []Event{ready, stopped}, 0, false, false},
		{"ready after applied", []Event{applied, ready}, 2, true, true},
	}
	for _, c := range cases {
		got, applicable, ok := latestResolutionReady(c.events)
		if got != c.want || applicable != c.wantApplies || ok != c.ok {
			t.Fatalf("%s: got (%d, %v, %v), want (%d, %v, %v)", c.name, got, applicable, ok, c.want, c.wantApplies, c.ok)
		}
	}
}

func TestLatestResolutionReadyDecodesJournalEvents(t *testing.T) {
	cases := []struct {
		name        string
		applicable  bool
		wantApplies bool
	}{
		{"applicable", true, true},
		{"not applicable", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, r := resolutionRun(t)
			startRun(t, p, r)
			if _, e := p.Append(r.ID, r.Workflow.Id, "", "resolution.ready", map[string]any{
				"ready":      2,
				"blocked":    0,
				"conflict":   0,
				"applicable": c.applicable,
				"changeIds":  []string{"chg_a", "chg_b"},
			}); e != nil {
				t.Fatal(e)
			}
			events, e := p.Events(r.ID)
			if e != nil {
				t.Fatal(e)
			}
			got, applicable, ok := latestResolutionReady(events)
			if got != 2 || applicable != c.wantApplies || !ok {
				t.Fatalf("got (%d, %v, %v), want (2, %v, true)", got, applicable, ok, c.wantApplies)
			}
		})
	}
}

func TestEventCountAcceptsJournalAndInProcessNumbers(t *testing.T) {
	if eventCount(3) != 3 || eventCount(float64(4)) != 4 || eventCount(int64(5)) != 5 || eventCount("x") != 0 {
		t.Fatal("eventCount did not decode numeric journal values")
	}
	if eventCount(json.Number("7")) != 7 || eventCount(json.Number("bad")) != 0 {
		t.Fatal("eventCount did not decode json.Number")
	}
}

func TestResolutionSkipParsesTail(t *testing.T) {
	skip, err := resolutionSkip([]string{"skip", "a", "b"})
	if err != nil || len(skip) != 2 || skip[0] != "a" || skip[1] != "b" {
		t.Fatalf("skip parse: %v %v", skip, err)
	}
	if _, err := resolutionSkip([]string{"skip"}); err == nil {
		t.Fatal("bare skip should be a usage error")
	}
	if _, err := resolutionSkip([]string{"a"}); err == nil {
		t.Fatal("unknown tail should be a usage error")
	}
}
