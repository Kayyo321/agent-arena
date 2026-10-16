# Provider integrations

Reviewed on 2026-10-15. Interface availability, authentication and inference access
are separate checks. An installed CLI or advertised model is not proof of account
access, quota, tool support or a successful paid request. Provider options remain
opaque in Arena envelopes; adapters translate only supported controls.

| Adapter | Interface and authentication | Verification in this workspace |
| --- | --- | --- |
| Codex | Installed CLI, local app-server stdio, provider-owned sign-in | CLI 0.162.0-alpha.17.2 inspected; real `gpt-6.1-sol` / `high` read-only workflow and live Arena MCP context search passed |
| Claude Code | `--bare -p`, streamed JSON, API-key or supported cloud auth | CLI 2.1.224 and flags inspected; no approved API/cloud credentials available for live inference |
| Gemini | Official Google GenAI Go SDK, AI Studio API-key or Vertex AI ADC | SDK v1.75.0 compiled; live inference requires configured approved credentials |
| Local | Same-machine Ollama or OpenAI-compatible HTTP server | Contract fixtures; a real backend/model still needs user selection and verification |

## OpenAI

Arena invokes the installed Codex CLI and uses its documented stdio protocol for
model discovery, threads, turns, streaming and approval requests. Authentication
stays with Codex. Arena does not read auth files, implement an identity-provider
flow, copy tokens, pool accounts or bypass limits. MCP tools expose workflow-scoped
messaging, context proposals and user questions. Undocumented server requests are
refused. The local Arena bridge is discovered before inference; only its empty tool-call confirmations are accepted automatically. External servers, secret inputs and explicit sensitive/automatic-review requirements are refused. Native Codex sandbox defaults are supported; Arena refuses stricter shell
policies that this adapter cannot enforce.

The observed CLI is an alpha build. Compatibility remains capability-based, and
new versions are reported as drift until inspected. Model catalogs can be bundled,
so status marks advertised access as unverified. The live test explicitly opts into
provider usage and is excluded from normal test runs.

Setup uses the same documented app-server worker interface in the background:
Arena owns the transcript and renders questions in its TUI, using a read-only
structured interview. A completed draft is validated and saved only by Arena
after `/save`. No native Codex terminal interface is launched. Public tool/status
events and commentary are projected into agent windows; reasoning item content
and raw thought streams are ignored. See the current official
[app-server client lifecycle and events](https://learn.chatgpt.com/docs/app-server).

References: [Codex integration interfaces](https://developers.openai.com/blog/codex-as-a-platform),
[app-server protocol](https://learn.chatgpt.com/docs/app-server),
[Sign in with ChatGPT terms](https://openai.com/policies/sign-in-with-chatgpt-terms/),
[official MCP approval metadata](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/mcp_approval_meta.rs).

## Anthropic

Anthropic's current guidance directs third-party products to API-key or supported
cloud authentication. Arena therefore requires `ANTHROPIC_API_KEY` or a documented
Claude Code cloud route. It uses bare mode and does not rely on subscription OAuth.
Provider-native tools are disabled for worker calls; Arena's bounded tool protocol
mediates file access, commands, packets and questions. Commands require the project's
policy and a supported OS sandbox. Credentials are never included in those command
environments. Each command gets private temporary/home/cache directories that are removed after supervision ends; other worktrees and global temporary directories remain unwritable, and only system toolchain roots, the assigned worktree and resolved toolchain directories are readable (no home directories, other worktrees or host processes; see [Architecture](ARCHITECTURE.md#mediated-command-sandbox)). Networking is disabled. Dependency downloads must be prepared outside the run; existing module caches are read-only. Model/effort access stays unverified until actually exercised.

References: [programmatic CLI](https://code.claude.com/docs/en/headless),
[authentication restrictions](https://code.claude.com/docs/en/legal-and-compliance).

## Google

Gemini uses the official Google GenAI SDK. Select `google-ai-studio-api-key` with
provider-supported environment configuration, or `vertex-ai` with project/location
and Google-managed ADC. The SDK owns auth handling; Arena never journals credentials.
Personal Gemini CLI OAuth is not an Arena path, including when a signed-in Gemini
CLI happens to be installed. Status asks the selected API/Vertex route for the named
model; it does not fall back to another auth method or model.

`options.generation` accepts documented SDK generation controls. Transport overrides,
external tools, caches and replacement system instructions are refused. Automatic
billable continuation is disabled. Arena does not persist model thought content.

References: [official SDKs](https://ai.google.dev/gemini-api/docs/libraries),
[Vertex ADC](https://cloud.google.com/vertex-ai/generative-ai/docs/start/gcp-auth),
[Gemini CLI FAQ boundary](https://geminicli.com/docs/faq/).

## Local inference

Local endpoints must be loopback HTTP URLs without embedded credentials or redirect
chains. Models are discovered through Ollama `/api/tags` or an OpenAI-compatible
`/models` endpoint. Chat responses stream through a bounded JSON tool protocol.
Arena does not assume native function calling or a universal reasoning scale. Local
backends use the same workflow, messages, context, worktrees and integration queue.
Ollama generation values stay in `options.generation`; OpenAI-compatible generation
controls are limited to documented request fields. Unknown capabilities remain unknown.

References: [Ollama chat](https://docs.ollama.com/api/chat),
[LM Studio compatibility](https://lmstudio.ai/docs/developer/openai-compat/tools).

## Skill-only hosts

Copilot and OpenCode receive the same portable skill. No execution adapter or account
access is advertised for either host. Locations follow the hosts' documented discovery
paths: [Copilot customization](https://docs.github.com/en/copilot/reference/customization-cheat-sheet),
[OpenCode skills](https://docs.opencode.ai/docs/skills/),
[Gemini skill locations](https://geminicli.com/docs/cli/using-agent-skills/).

## Running live verification

Only run this after explicitly selecting a model/effort and accepting provider usage:

```sh
make build
ARENA_INTEGRATION_BINARY="$PWD/bin/arena" \
ARENA_LIVE_CODEX_MODEL='YOUR_SELECTED_MODEL' \
ARENA_LIVE_CODEX_EFFORT='YOUR_SELECTED_EFFORT' \
go test ./internal/arena -run TestLiveCodex -v -count=1
```

These are real inference tests. Other adapter/HTTP contract fixtures are labelled in
tests and do not establish live readiness. The M3 multi-provider exit check remains
pending until a second approved route and model are supplied and exercised.
