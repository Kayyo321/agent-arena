package arena

import (
	"fmt"
	"os"
	"strings"
)

// HistoryEntry is one run in `arena history --all`.
type HistoryEntry struct {
	ID       string `json:"id"`
	Imported bool   `json:"imported"`
}

// historyAudit handles the audit-bundle and purge forms of `arena history`,
// the `--all` run list, and imported runs. done is false for every other form,
// including the plain and `--json` lists, which stay the array of local run IDs.
func (cli *CLI) historyAudit(p *Project, args []string, o Options) (done bool, err error) {
	switch {
	case len(args) == 1 && o.Bool("all"):
		local, err := p.Runs()
		if err != nil {
			return true, err
		}
		imported, err := p.ImportedRuns()
		if err != nil {
			return true, err
		}
		entries := []HistoryEntry{}
		for _, id := range local {
			entries = append(entries, HistoryEntry{ID: id})
		}
		for _, id := range imported {
			entries = append(entries, HistoryEntry{ID: id, Imported: true})
		}
		cli.print(entries)
		return true, nil
	case len(args) == 3 && args[1] == "import":
		m, err := p.ImportAudit(args[2])
		if err != nil {
			return true, err
		}
		fmt.Fprintf(cli.Out, "Imported %s read-only (%d events, retention %s) under %s; it cannot be resumed and no live state was changed.\n", m.RunID, m.EventCount, m.Retention, p.importedDir(m.RunID))
		return true, nil
	case len(args) == 4 && args[2] == "export":
		m, err := p.ExportAudit(args[1], args[3])
		if err != nil {
			return true, err
		}
		fmt.Fprintf(cli.Out, "Exported %s to %s (%d events, %d files, retention %s", m.RunID, args[3], m.EventCount, len(m.Files), m.Retention)
		if len(m.Omitted) > 0 {
			fmt.Fprintf(cli.Out, "; omitted %s", strings.Join(m.Omitted, " and "))
		}
		fmt.Fprintln(cli.Out, "). Review it before sharing; secret scanning is best-effort.")
		return true, nil
	case len(args) == 3 && args[2] == "purge":
		return true, cli.purge(p, args[1], o)
	case len(args) == 2:
		if _, err := os.Lstat(p.RunDir(args[1])); err == nil || validRunID(args[1]) != nil {
			return false, nil
		}
		events, err := p.ImportedEvents(args[1])
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return true, err
		}
		if !o.Bool("json") {
			for _, event := range events {
				fmt.Fprintf(cli.Out, "%d %s %s %s\n", event.Sequence, event.At, event.AgentID, event.Type)
			}
			return true, nil
		}
		history, err := p.History(args[1], events)
		history.Imported = true
		cli.print(history)
		return true, err
	}
	return false, nil
}

func (cli *CLI) purge(p *Project, id string, o Options) error {
	plan, err := p.PlanPurge(id)
	if err != nil {
		return err
	}
	cli.print(plan)
	fmt.Fprintln(cli.Out)
	if len(plan.Unintegrated) > 0 {
		fmt.Fprintf(cli.Out, "Unintegrated work that purging would lose:\n  %s\n", strings.Join(plan.Unintegrated, "\n  "))
	}
	if !o.Bool("yes") {
		return fmt.Errorf("purge permanently deletes the items above and cannot be undone; review them, then add --yes")
	}
	if _, err := p.PurgeRun(id, o.Bool("discard-unintegrated")); err != nil {
		return err
	}
	fmt.Fprintf(cli.Out, "Purged %s; a tombstone is in the project journal. Git objects are left for git gc.\n", id)
	return nil
}
