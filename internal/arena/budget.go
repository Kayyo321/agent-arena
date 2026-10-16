package arena

import (
	"context"
	"fmt"
	"time"
)

// runMinutes is how long the run has been working: wall-clock time between
// workflow.started/resumed and the next pause, interruption or end, so time
// spent waiting on the user (a paused run) is not charged to the budget.
func runMinutes(events []Event, now time.Time) float64 {
	var total time.Duration
	var since time.Time
	for _, v := range events {
		at, err := time.Parse(time.RFC3339Nano, v.At)
		if err != nil {
			continue
		}
		switch v.Type {
		case "workflow.started", "workflow.resumed":
			since = at
		case "workflow.paused", "workflow.interrupted", "workflow.cancelled", "workflow.failed", "workflow.succeeded":
			if !since.IsZero() {
				total += at.Sub(since)
				since = time.Time{}
			}
		}
	}
	if !since.IsZero() {
		total += now.Sub(since)
	}
	return total.Minutes()
}

// exceededBudget names the first optional run budget the journal shows as
// reached and not yet continued past by the user. The limits are off unless
// limits.maxRunTokens or limits.maxRunMinutes is set.
func (e *Engine) exceededBudget(events []Event) (kind string, limit, used int64) {
	limits := e.Run.Settings.Limits
	if limits.MaxRunTokens == nil && limits.MaxRunMinutes == nil {
		return "", 0, 0
	}
	continued := map[string]bool{}
	for _, v := range events {
		if v.Type == "budget.continued" {
			continued[str(v.Data["budget"])] = true
		}
	}
	if n := limits.MaxRunTokens; n != nil && !continued["tokens"] {
		if total := SumUsage(events).Total(); total >= int64(*n) {
			return "tokens", int64(*n), total
		}
	}
	if n := limits.MaxRunMinutes; n != nil && !continued["minutes"] {
		now := time.Now()
		if e.Now != nil {
			now = e.Now()
		}
		if minutes := runMinutes(events, now); minutes >= float64(*n) {
			return "minutes", int64(*n), int64(minutes)
		}
	}
	return "", 0, 0
}

// enforceBudget runs before the scheduler launches the next node. A tripped
// budget never kills the run: it journals budget.exceeded, pauses the run and
// raises a decision. "continue" lifts that budget for the rest of the run and
// resumes; "stop" leaves the run paused, so the scheduler waits and asks again
// if the user resumes it. It reports whether the node may launch now.
func (e *Engine) enforceBudget(ctx context.Context) (bool, error) {
	events, err := e.Project.Events(e.Run.ID)
	if err != nil {
		return false, err
	}
	kind, limit, used := e.exceededBudget(events)
	if kind == "" {
		return true, nil
	}
	if _, err := e.event("", "budget.exceeded", map[string]any{"budget": kind, "limit": limit, "used": used}); err != nil {
		return false, err
	}
	state, err := e.Project.State(e.Run.ID)
	if err != nil {
		return false, err
	}
	if state.Status == "running" {
		if _, err := e.event("", "workflow.paused", map[string]any{"reason": "budget " + kind}); err != nil {
			return false, err
		}
	}
	setting := map[string]string{"tokens": "limits.maxRunTokens", "minutes": "limits.maxRunMinutes"}[kind]
	answer, err := e.Project.Decide(ctx, e.Run, "arena", "budget", fmt.Sprintf("Run budget reached: %d of %d %s (%s). Continue lifts this budget for the rest of the run; stop leaves the run paused.", used, limit, kind, setting), []string{"continue", "stop"}, map[string]any{"budget": kind, "limit": limit, "used": used})
	if err != nil {
		return false, err
	}
	if answer != "continue" {
		return false, nil
	}
	if _, err := e.event("", "budget.continued", map[string]any{"budget": kind}); err != nil {
		return false, err
	}
	// The user may have resumed the run while the question was open.
	if state, err = e.Project.State(e.Run.ID); err != nil {
		return false, err
	}
	if state.Status == "paused" {
		if _, err := e.event("", "workflow.resumed", map[string]any{"reason": "budget continued"}); err != nil {
			return false, err
		}
	}
	return true, nil
}
