package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// followInterval is how often a follower re-reads the journal; it is the live
// view's tick (tickUI).
const followInterval = 150 * time.Millisecond

// followEvents hands a run's journal events to emit in sequence order. With
// follow it keeps polling the journal the way LiveModel.uiTick does, so the
// observer stays a projection of the journal: the only state it keeps is the
// last sequence it delivered, which is what keeps a poll from repeating or
// reordering events.
//
// It ends once the run is terminal and a poll finds nothing new (a cancelled
// run's agents still record agent.cancelled after workflow.cancelled), when the
// events do not replay, or when the journal no longer holds what was already
// delivered. Cancelling ctx, which is how an interrupt arrives, ends it
// without error. Without follow it delivers the journal once.
func (p *Project) followEvents(ctx context.Context, runID string, follow bool, emit func(Event) error) error {
	ticker := time.NewTicker(followInterval)
	defer ticker.Stop()
	last := 0
	for {
		events, err := p.Events(runID)
		if err != nil {
			return err
		}
		// Events checks that the journal is contiguous, so the newest sequence
		// is the event count.
		if len(events) < last {
			return fmt.Errorf("run %s history shrank from %d to %d events; it may have been retired", runID, last, len(events))
		}
		fresh := false
		for _, e := range events {
			if e.Sequence <= last {
				continue
			}
			if err := emit(e); err != nil {
				return err
			}
			last, fresh = e.Sequence, true
		}
		if !follow {
			return nil
		}
		state, err := Reduce(events)
		if err != nil {
			return fmt.Errorf("run %s events do not replay: %w", runID, err)
		}
		if !fresh && Has(terminalStatuses, state.Status) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// emitLine writes one line and flushes it, so a reader on a pipe sees each
// line as it is recorded.
func (cli *CLI) emitLine(line string) error {
	if _, err := fmt.Fprintln(cli.Out, line); err != nil {
		return err
	}
	if f, ok := cli.Out.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// streamEvents is `watch RUN --json`: one journal event per line, exactly as
// events.ndjson records it so the hash chain can still be checked.
func (cli *CLI) streamEvents(ctx context.Context, p *Project, runID string, follow bool) error {
	return p.followEvents(ctx, runID, follow, func(e Event) error {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		return cli.emitLine(string(line))
	})
}

// rawOutputNote says why a run's process output is missing from an agent's
// activity; it is empty when Arena kept the output.
func rawOutputNote(r Run) string {
	if r.Retention == "full-local" && r.Settings.Audit.ProcessOutput == "full" {
		return ""
	}
	return fmt.Sprintf("Raw process output is not retained for this run (retention %s, process output %s); only sizes are shown", r.Retention, r.Settings.Audit.ProcessOutput)
}

// viewAgent is `view AGENT --run RUN`: the activity window of the live view as
// plain text for a terminal pane. With --follow new activity is appended until
// the run ends, as `watch --follow` does.
func (cli *CLI) viewAgent(ctx context.Context, p *Project, agent string, o Options) error {
	if o.Get("run") == "" {
		return fmt.Errorf("view AGENT --run RUN [--follow]")
	}
	r, err := p.ReadRun(o.Get("run"))
	if err != nil {
		return err
	}
	events, err := p.Events(r.ID)
	if err != nil {
		return err
	}
	known := false
	ids := []string{}
	for _, a := range r.Workflow.Agents {
		known = known || a.Id == agent
		ids = append(ids, a.Id)
	}
	for _, e := range events {
		known = known || e.AgentID == agent
	}
	if !known {
		return fmt.Errorf("run %s has no agent %q; agents: %s", r.ID, agent, strings.Join(ids, ", "))
	}
	follow := o.Bool("follow")
	state, err := Reduce(events)
	if err != nil && !follow {
		fmt.Fprintf(cli.Err, "arena: run %s events do not replay: %v\n", r.ID, err)
	}
	m := LiveModel{Run: r, State: state, Events: events}
	header := m.agentHeader(agent)
	if note := rawOutputNote(r); note != "" {
		header = append(header, note)
	}
	for _, line := range append(header, "") {
		if err := cli.emitLine(terminalSafe(Redact(line))); err != nil {
			return err
		}
	}
	var seen []Event
	printed := 0
	err = p.followEvents(ctx, r.ID, follow, func(e Event) error {
		seen = append(seen, e)
		if e.AgentID != agent {
			return nil
		}
		line := activityLine(e)
		if line == "" {
			return nil
		}
		printed++
		return cli.emitLine(terminalSafe(Redact(line)))
	})
	// An interrupt ends a follow without the closing summary.
	if err != nil || ctx.Err() != nil {
		return err
	}
	if printed == 0 {
		if err := cli.emitLine("No recorded activity yet"); err != nil {
			return err
		}
	}
	m.State, _ = Reduce(seen)
	tail := m.structuredResult(agent)
	if follow {
		tail = append(tail, "Run "+m.State.Status)
	}
	for _, line := range tail {
		if err := cli.emitLine(terminalSafe(Redact(line))); err != nil {
			return err
		}
	}
	return nil
}
