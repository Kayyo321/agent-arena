package arena

// Approval records which Arena entry point received an approval and what was
// verified about who gave it. Arena cannot tell a person at a keyboard from a
// process that drives the same terminal, TUI or command line, so no approval
// carries a verified identity: IdentityVerified is false on every record and
// Channel is the only fact claimed. A journal "actor" of "user" on these events
// is the legacy label for a human-facing entry point, not an identity.
//
// The separation that would make an approval mean "a human decided" is a
// proposed boundary, not an implemented one; see
// docs/adr/0002-operator-approval-boundary.md.
type Approval struct {
	Channel string
}

const (
	// ApprovalCLITerminal is `arena decision answer`, `arena integrate` or
	// `arena rollback` with a terminal on stdin and stdout.
	ApprovalCLITerminal = "cli-terminal"
	// ApprovalCLINoTerminal is the same commands run from a pipe, a script or
	// another program's command runner.
	ApprovalCLINoTerminal = "cli-no-terminal"
	// ApprovalTUI is an answer typed into the live view.
	ApprovalTUI = "tui"
	// ApprovalLibrary is a direct call into the arena package, with no
	// user-facing entry point (embedding, tests).
	ApprovalLibrary = "library"
)

// Record is the journal form of the approval.
func (a Approval) Record() map[string]any {
	return map[string]any{"channel": a.Channel, "identityVerified": false, "verification": "none"}
}

// cliApproval names the channel of a command run through the CLI.
func (cli *CLI) cliApproval() Approval {
	if cli.isTerminal() {
		return Approval{Channel: ApprovalCLITerminal}
	}
	return Approval{Channel: ApprovalCLINoTerminal}
}
