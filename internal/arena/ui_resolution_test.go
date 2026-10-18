package arena

import "testing"

func TestLatestResolutionReadyFollowsNewestResolutionEvent(t *testing.T) {
	ready := Event{Type: "resolution.ready", Data: map[string]any{"ready": float64(2)}}
	applied := Event{Type: "resolution.applied"}
	stopped := Event{Type: "resolution.stopped"}
	cases := []struct {
		name   string
		events []Event
		want   int
		ok     bool
	}{
		{"none", []Event{{Type: "workflow.succeeded"}}, 0, false},
		{"ready", []Event{{Type: "workflow.succeeded"}, ready}, 2, true},
		{"applied after ready", []Event{ready, applied}, 0, false},
		{"stopped after ready", []Event{ready, stopped}, 0, false},
		{"ready after applied", []Event{applied, ready}, 2, true},
	}
	for _, c := range cases {
		got, ok := latestResolutionReady(c.events)
		if got != c.want || ok != c.ok {
			t.Fatalf("%s: got (%d, %v), want (%d, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestEventCountAcceptsIntAndFloat(t *testing.T) {
	if eventCount(3) != 3 || eventCount(float64(4)) != 4 || eventCount(int64(5)) != 5 || eventCount("x") != 0 {
		t.Fatal("eventCount did not decode numeric journal values")
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
