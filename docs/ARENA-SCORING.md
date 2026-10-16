# Arena scoring

Arena grades every arena against this score sheet (rubric v3). The score is computed by Arena, not by a model, from the workflow and its agent profiles only. Project settings, provider readiness and the background shader do not change it, so the person who shares an arena and the person who pastes it see the same grade.

Capabilities count only where the agent's provider adapter can exercise them. These adapter limits are fixed per provider and runtime options in Arena, not probed, so they keep the score a pure function of the arena. Today one applies: Antigravity's headless mode cannot execute commands, so `run_commands` on an Antigravity agent earns nothing (it does not satisfy V5, for example) and costs C6, unless that agent's runtime sets `options.commandApproval: "sandboxed-blanket"` (see [Antigravity command approval](CONFIGURATION.md#antigravity-command-approval)). With the option the grant counts like any other provider's. Every arena that was valid before the option existed scores the same, so the rubric version is unchanged. `arena workflow validate` and `arena workflow create` print a warning for each such grant, because that agent fails when it starts.

Rubric v3 counts fan-out nodes at their cap: a fan-out stands for its planner plus `maxChildren` worker instances wherever the sheet counts agents. Arenas without a fan-out score exactly as they did under v2.

The grade appears in the setup conversation when a draft validates, in the CLI before `arena workflow create` confirms, in the launcher, at the top of the live view, and in full with `/score` or `arena score ID`.

## What a good arena looks like

- **No single point of trust.** Whoever changes code is not the one who approves it, and the approver cannot quietly edit what it approves.
- **Independent eyes.** At least one reviewer runs on a different provider or model than the agent it reviews, so the two do not share blind spots.
- **Rejections lead somewhere.** A `ChangesRequested` verdict loops back to the writer, within a small cap, instead of ending the run.
- **Every agent earns its place.** Work that one agent can do is done by one agent. Relays, duplicates and deep hierarchies cost points.
- **Each role is legible.** A stranger reading a profile knows what the agent does and what it must not touch.

## Terms

- **Writer:** a path agent whose profile has `write`, a team with a member that has `write` (the team node carries that member's change), or a fan-out whose worker has `write` (the fan-out node carries every child's change).
- **Review gate:** a path agent with role `review`, a team with at least one member whose role is `review` or who has the `review` capability, or a fan-out whose worker has that role or capability. A gate's reviewers are the agent itself, the team's members, or the fan-out's worker.
- **Path agents:** standalone path agents and team members, plus each fan-out's planner and `maxChildren` worker instances.
- **Forward edges:** `delegates` relationships, non-loop router routes and `route` fallbacks. These are the edges along which Arena hands a node's result packet to the next node. Loop routes are back edges.
- **Reviewed:** a review gate is reachable from the writer along forward edges, so the gate receives the writer's change. A reviewer that is the writer's sibling does not receive the change and does not count.
- **Independent:** at least one of a gate's reviewers differs from the reviewed agent in provider or model.
- **Revision loop:** a router that consumes `review-result` or `team-result`, downstream of the writer, has a `loop: true` route back to the writer or one of its ancestors.
- **Worst-case invocations:** path agents (with every fan-out at `maxChildren`), plus each loop's `maxIterations` × the agents in its loop body (a fan-out in the body counts as its planner plus `maxChildren`), plus team members × `maxRounds`, plus every on-call cap. The project's `limits.maxFanOutChildrenPerRun` can stop a run sooner, but it is a project setting and does not change the grade.
- **Redundant:** same role, same capability set, and a word-overlap (Jaccard) score of at least 0.6 between objective + instructions, after common words are dropped.

## Categories and checks

The five categories add up to 100 points. Each finding deducts points from its category, and a category cannot go below zero. A check that does not apply (no teams, no writers) costs nothing; simple arenas are not penalized for missing machinery they do not need.

### T — Trust distribution (30)

*Does any single agent hold too much unchecked power?*

| ID | Check | Deduction | Severity |
|---|---|---|---|
| T1 | A writer with no review gate downstream | −12 each, at most −24 | major |
| T2 | An agent that delegates work to another non-review agent and also has `write` and `run_commands` (not counted in a one-agent arena) | −8 each | major |
| T3 | A reviewer, or a member of a review team, with `write` | −6 each | major |
| T4 | Every reviewer of a writer runs on the writer's provider and model | −4 per writer | moderate |
| T5 | `integration.policy: auto-if-clean` | −15 with an unreviewed writer (major), otherwise −3 | major / moderate |
| T6 | Least-privilege violation: a writer that also delegates writing to another writer; `write` on a research, planning, architecture or analysis role; `run_commands` on an agent that neither writes nor verifies | −2 each, at most −8 | minor |
| T7 | A team whose members all share one provider and model | −3 each | moderate |

### P — Proportionality (25)

*Is every agent earning its place?*

| ID | Check | Deduction | Severity |
|---|---|---|---|
| P1 | Two redundant agents outside the same team | −6 per pair | moderate |
| P2 | A relay: a non-head path agent with one parent and one agent child, that neither writes nor verifies, and whose capabilities the child already has | −5 each | moderate |
| P3 | Deepest level above 3 (more than four tiers) | −3 per extra level | moderate |
| P4 | `maxConcurrentAgents` above the number of path agents, or above 1 but below the widest fan-out: the most forward edges out of one node, or the largest fan-out `maxChildren` (a single slot is R3) | −2 | minor |
| P5 | A team with more than 3 members, or more than 3 deliberation rounds | −2 each | minor |
| P6 | A router with no loop route whose branches all lead to one place (one route and a `stop` fallback, or every route to the same node) | −3 each | moderate |
| P7 | An on-call agent redundant with a path agent, or an on-call cap above 3 | −2 each | minor |
| P8 | Worst-case invocations above 6 × path agents, or above 40 | −4 | moderate |
| P9 | Several agents and none of them writes or reviews: read-only fan-out one agent could do | −5 | moderate |

### V — Verification & feedback (20)

*Is work checked, and can it be fixed?*

| ID | Check | Deduction | Severity |
|---|---|---|---|
| V1 | Writers exist and the arena has no review gate at all | −10 | major |
| V2 | A reviewed writer with no revision loop | −4 per writer | moderate |
| V3 | A loop route with `maxIterations: 5` (−1), or a loop on a router that does not consume a review or team verdict (−3) | −1 / −3 each | minor / moderate |
| V4 | A router fallback that routes to an unreviewed writer | −3 each | moderate |
| V5 | Writers and reviewers exist but no reviewer has `run_commands` without `write`, so nobody can run the tests impartially | −3 | moderate |
| V6 | Writers or teams exist and no on-call rule fires on `team.no-consensus`, `agent.failed` or `validation.failed` | −2 | minor |
| V7 | A reviewer whose reasoning effort ranks below the writer's (none < minimal < low < medium < high < xhigh < max) | −1 per pair | minor |

### C — Role clarity (15)

*Would a stranger know exactly what each agent does?*

| ID | Check | Deduction | Severity |
|---|---|---|---|
| C1 | An objective under 8 words or instructions under 25 words | −2 each, at most −8 | minor |
| C2 | A writer with no `constraints` bounding what it may change | −2 each | minor |
| C3 | A generic agent ID or name such as `agent1`, `worker-2` or `bot` | −1 each | minor |
| C4 | The head agent has role `review`, so the run starts with nothing to review | −3 | moderate |
| C5 | The arena has no `description` | −1 | minor |
| C6 | A capability the agent's provider adapter cannot exercise, such as `run_commands` on Antigravity without `commandApproval: "sandboxed-blanket"` | −2 each | minor |

### R — Resilience & cost (10)

| ID | Check | Deduction | Severity |
|---|---|---|---|
| R1 | Three or more agents, all on one provider: one outage stops the arena | −2 | minor |
| R2 | Writers exist and an on-call rule has no event trigger, only `manual` | −1 each | minor |
| R3 | A node fans out to parallel branches, or a fan-out allows more than one child, but `maxConcurrentAgents` is 1 | −1 | minor |
| R4 | Worst-case invocations above 60 | −4 | moderate |

## Grades

Minimum points for each grade:

| | + | plain | − |
|---|---|---|---|
| S | 97 | 93 | 90 |
| A | 87 | 83 | 80 |
| B | 77 | 73 | 70 |
| C | 67 | 63 | 60 |
| D | 57 | 53 | 50 |

Below 50 is F. Points alone set the **band**. The grade is the band, lowered by these rules.

### Ceilings

| Finding | Highest grade |
|---|---|
| T1, an unreviewed writer | B |
| T5 with an unreviewed writer | C |
| T2, a coordinator that also writes and runs commands | A− |
| T3, a reviewer that can write | A− |
| P1 or P2, a redundant or relay agent | A |

### S-tier gate (S−, S, S+)

An arena whose band is S−, S or S+ drops to A+ unless all of these hold:

- no T1, T3, T4, T6, P1, P2 or V2 finding: every writer is reviewed independently, every writer can be sent back for revision, reviewers cannot write, and no agent is redundant, a relay or over-privileged;
- every category keeps at least 85% of its points.

### S+ gate

An S+ band drops to S unless, in addition:

- at least one review gate independently reviews another agent's work, so a lone agent tops out at S;
- every category keeps at least 90% of its points;
- the arena has at most one finding, and that finding is minor.

`/score` and `arena score ID` list what the next tier still needs.

## Designing toward the sheet

The orchestrator receives this sheet and the score of each draft it proposes. It designs toward a high grade but does not add agents for points: proportionality is part of the score, and an extra agent that does not earn its place costs more than it gains. When a deduction comes from an explicit user choice, such as one provider for every agent, the orchestrator keeps the choice and names the deduction.

A typical S+ arena:
- a lead that plans and delegates but does not write;
- an implementer with `write`, `run_commands` and constraints;
- a reviewer on another provider with `read`, `review` and `run_commands`, but no `write`;
- a router on the reviewer's `/outcome` that loops `ChangesRequested` back to the implementer, capped at 2 or 3;
- an on-call maintainer woken by `agent.failed` or `validation.failed`.
