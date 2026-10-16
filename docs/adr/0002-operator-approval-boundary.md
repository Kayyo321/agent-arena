# ADR 0002: Operator approval boundary

Status: proposed. Not implemented. The finding it answers (an agent approving its own integrate or rollback request) is **partially mitigated**; it is not closed, and no part of Arena enforces that a human approved anything.

## Problem

An integrate or rollback request, opened through the control server (`arena_integrate`, `arena_rollback`), waits for an answer. Arena executes it when the answer is `approve`. Nothing separates the party that may answer from the agents that may ask: they run as the same OS user on the same machine, and the answer path is reachable by any process that can run `arena decision answer`, type into the TUI, or write the decision and journal files under `.arena/.state/`.

## What exists today

None of these is a boundary.

1. **Terminal requirement.** `arena decision answer RUN ID approve` refuses an integrate or rollback request unless stdin and stdout are terminals. An agent with a pseudo-terminal passes this check. The TUI has no such check, and an agent that can reach the operator's terminal (a tmux server socket under `arena tmux`, `TIOCSTI`, a pty file descriptor) can type into it.
2. **Provenance.** Approval events record `approval: {channel, identityVerified: false, verification: "none"}` instead of `approvedBy: "user"`. The channels are `cli-terminal`, `cli-no-terminal`, `tui` and `library`. Arena claims the entry point and nothing else. A journal `actor` of `user` is the legacy label for a human-facing entry point, not an identity. Policy integration (`auto-if-clean`) still records `approvedBy: "policy:auto-if-clean"`.
3. **Operation binding.** The request's preview carries the patch digest (integrate) or the integration commit (rollback). Execution re-reads both and refuses on any difference, as it already did for `HEAD`. This catches a patch or commit that changed between the request and the answer. It does not stop a writer who can also rewrite the decision record.

## Threat model

The attacker is an agent: a provider process Arena starts, or a command it runs, acting on model output that may be steered by prompt injection. It runs as the operator's OS user. It wants an integrate or rollback to execute without a human decision, or the human to approve something other than what they reviewed.

An approval means "a human decided" only if all of these hold:

- **R1, unreachable authority.** No agent process can run the answer path with effect, write decision, change or journal records, or drive the operator's terminal (pty, tmux or screen socket, `TIOCSTI`, `/proc/PID/fd`, `ptrace`, signals to the supervisor).
- **R2, immutable operation.** What executes is what was reviewed, bound by a digest in a record the agent cannot write.
- **R3, honest record.** The record says which boundary held for this approval, so an approval made with no boundary is not reported as a human one.

## Why smaller additions do not establish it

- **Another confirmation prompt.** It is driven through the channel the agent can already reach.
- **An environment variable or flag** (an `ARENA_OPERATOR` marker), **or a secret in `.arena/` or the home directory.** Anything a same-user process can read, an agent that is not fenced can read.
- **The terminal check.** A pty satisfies it.
- **Detecting that the caller is an agent.** The absence of a marker proves nothing.

## Proposed boundary

Arena fences every agent process, not only the commands it mediates, and keeps the approval authority outside that fence. The answer path then fails because the state it needs is not visible or writable from where the agent runs, not because the code declines.

**A. Fence the provider process.** Launch each provider process (`claude`, `agy`, the Codex app server, Gemini and local-backend tool runners) under an Arena-owned fence built from the existing bubblewrap and `sandbox-exec` machinery (`sandbox.go`, `antigravity_sandbox.go`):

- `.arena/` outside the agent's own worktree is neither readable nor writable. The agent reaches Arena only through the MCP bridge on its stdio. The project's `.git` is not writable.
- A new PID namespace with a fresh `/proc`, so the agent cannot `ptrace`, signal or read the file descriptors of the supervisor or the operator's shell.
- A new session with its own devpts and no inherited controlling terminal, so it cannot reach the operator's pty.
- No tmux or screen socket, ssh-agent, D-Bus or X11 socket: a private `/tmp` and an empty `/run`, as the Antigravity fence already does.
- Provider sign-in state bound in only as each adapter needs it, read-only, as the Antigravity fence does with an emptied home.

**B. Bind the operation.** The request stores the reviewed preview, its patch digest and the head under `.arena/.state`, which is now outside the fence. The answering process, never the supervisor or an agent, writes `decision.answered`, and binds the answer to the decision ID, action, change ID, head and digest.

**C. Say which boundary held.** Each approval records `boundary: "arena-fence"` only when every agent in the run was launched under the fence, and `boundary: "none"` otherwise. `arena doctor` and `arena status` list each adapter as `fenced` or `provider-confined`. A project setting can refuse `arena_integrate` and `arena_rollback` for runs that include an unfenced adapter.

An additional proof of presence, such as FIDO2 user presence or an OS prompt (macOS LocalAuthentication, polkit), strengthens R1 without depending on the fence. It needs a trust anchor for the verifier that an agent cannot rewrite, which is the fence again, so it adds to the boundary rather than replacing it.

A separate OS principal (another uid, a container or a VM for agents) is stronger and out of scope: it needs privileges or a runtime that Arena's local-first, no-daemon model does not assume.

## Why this is not done yet

- It changes the launch path of every adapter. Today `claude` runs under Claude Code's own permission model, Codex under its native sandbox, and only Antigravity's blanket mode has an outer fence; confinement on the other routes is the provider's, not Arena's.
- Nesting Arena's fence around providers that have their own sandbox, and binding their sign-in state, has to be exercised live per provider, and the macOS profile has not been run on a macOS host.

## Verification required before this is called enforced

From inside the fence, with a planted pending decision, every one of these fails: running `arena decision answer`, writing the decision or journal files, opening the operator's pty or tmux socket, and `ptrace` or signalling the supervisor. The same checks pass for the unfenced providers only as documented gaps, and `boundary` reads `none` for runs that use them. Each adapter's fenced launch needs a live pass recorded in [validation evidence](../VALIDATION.md).

## Consequences

Until this is accepted and implemented, documentation says a request "waits for an answer", not that a human approved it. Arena records the entry point and states that no identity was verified.
