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

// awaitBudget gates one agent invocation on the run budgets: it returns once
// no budget is reached, or the user continued past it, waiting out a pause
// (a stop answer) and asking again on resume.
func (e *Engine) awaitBudget(ctx context.Context) error {
	for {
		launch, err := e.enforceBudget(ctx)
		if err != nil || launch {
			return err
		}
		if err := e.Project.AwaitScheduling(ctx, e.Run.ID); err != nil {
			return err
		}
	}
}

// reachedBudget names the budget a launch would run past, without asking
// anything or pausing the run; it is empty when none is reached. It is for
// launches that cannot wait for a resume (on-call agents run beside a paused
// or failed run).
func (e *Engine) reachedBudget() (kind string, limit, used int64, err error) {
	if e.Run.Settings.Limits.MaxRunTokens == nil && e.Run.Settings.Limits.MaxRunMinutes == nil {
		return "", 0, 0, nil
	}
	events, err := e.Project.Events(e.Run.ID)
	if err != nil {
		return "", 0, 0, err
	}
	kind, limit, used = e.exceededBudget(events)
	return kind, limit, used, nil
}

// enforceBudget runs before the scheduler launches the next node, and before
// every agent invocation and approved retry (awaitBudget). A tripped budget
// never kills the run: it journals budget.exceeded, pauses the run and raises a
// decision. "continue" lifts that budget, and only that one, for the rest of the
// run and resumes; if the other budget is also reached it is asked about next,
// before anything launches. "stop" leaves the run paused, so the scheduler waits
// and asks again if the user resumes it. It reports whether the launch may
// proceed now.
//
// A budget is checked against what has been journaled so far, at the moment a
// launch is about to start. Work already running is never interrupted, so it can
// take usage past the limit, and a provider that reports no usage adds nothing:
// this is a gate on starting more work, not a cap on spending.
func (e *Engine) enforceBudget(ctx context.Context) (bool, error) {
	// Budgets are off by default; skip the journal read then.
	if e.Run.Settings.Limits.MaxRunTokens == nil && e.Run.Settings.Limits.MaxRunMinutes == nil {
		return true, nil
	}
	// The scheduler and every agent invocation (fan-out children, team
	// members) come through here concurrently; one at a time means a tripped
	// budget raises one question, not one per caller.
	e.budgetMu.Lock()
	defer e.budgetMu.Unlock()
	for {
		events, err := e.Project.Events(e.Run.ID)
		if err != nil {
			return false, err
		}
		kind, limit, used := e.exceededBudget(events)
		if kind == "" {
			return true, nil
		}
		state, err := e.Project.State(e.Run.ID)
		if err != nil {
			return false, err
		}
		if state.Status == "paused" {
			// Paused already, by the user or by a stop answer to this same
			// budget: wait for the resume rather than asking again now.
			return false, nil
		}
		// A supervisor that was interrupted while this question was open left
		// it pending; the resumed run waits on that one instead of asking again.
		open, err := e.openBudgetDecision(kind)
		if err != nil {
			return false, err
		}
		if open == nil {
			if _, err := e.event("", "budget.exceeded", map[string]any{"budget": kind, "limit": limit, "used": used}); err != nil {
				return false, err
			}
		}
		if state.Status == "running" {
			if _, err := e.event("", "workflow.paused", map[string]any{"reason": "budget " + kind}); err != nil {
				return false, err
			}
		}
		var answer string
		if open != nil {
			answer, err = e.Project.awaitOpenDecision(ctx, e.Run, open.ID)
		} else {
			setting := map[string]string{"tokens": "limits.maxRunTokens", "minutes": "limits.maxRunMinutes"}[kind]
			answer, err = e.Project.Decide(ctx, e.Run, "arena", "budget", fmt.Sprintf("Run budget reached: %d of %d %s (%s). Continue lifts this budget for the rest of the run; stop leaves the run paused.", used, limit, kind, setting), []string{"continue", "stop"}, map[string]any{"budget": kind, "limit": limit, "used": used})
		}
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
	}
}

// openBudgetDecision finds a pending question about kind, if any.
func (e *Engine) openBudgetDecision(kind string) (*Decision, error) {
	open, err := e.Project.Decisions(e.Run.ID)
	if err != nil {
		return nil, err
	}
	for _, d := range open {
		if d.Kind == "budget" && str(d.Context["budget"]) == kind {
			return &d, nil
		}
	}
	return nil, nil
}
