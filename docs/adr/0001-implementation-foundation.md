# ADR 0001: Implementation foundation

Status: accepted by the project owner, 2026-10-15.

Arena is implemented in Go, with Bubble Tea for the terminal interface. Distribution
is standalone executables for macOS and Linux, including Linux under WSL2; users do
not install a language runtime. Native Windows is outside this release's platform
scope. The build toolchain is pinned in `go.mod` and CI rather than imposed on users.

The existing JSON Schemas remain the public contracts. A small repository-owned
generator produces Go models and validators. Validation does not require a runtime
JSON Schema dependency. Generated output is checked in and reproducibility is
checked in tests/CI. Graph and policy invariants are checked separately.

Portable atomic lock directories live inside `.arena/`. All managed worker
operations resolve the original project control directory, so separate worktrees
do not acquire unrelated locks. Locks record ownership and are never stolen solely
because they are old. Recovery requires evidence that the owner stopped, or an
explicit user recovery operation. Network filesystems are outside the supported
local-only storage contract.

Provider processes use attached supervision. Arena stops scheduling before
cancelling work, requests graceful termination, and bounds the wait before stopping
the process group. Interrupted runs preserve their event history and worktrees;
resuming a model request requires an explicit retry decision.

The first OpenAI integration invokes the installed Codex CLI through its documented
local interfaces. Authentication remains owned by Codex. Arena does not implement
Sign in with ChatGPT, read credential files, copy tokens, or add a partner identity.
Provider compatibility and readiness are separate from fixture/mock test coverage.

Provider references reviewed during implementation are recorded in
`docs/PROVIDERS.md`. No push, publication, or deployment is authorized by this ADR.
