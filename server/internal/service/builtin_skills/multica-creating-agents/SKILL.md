---
name: multica-creating-agents
description: "Use when creating, inspecting, or debugging a Multica agent through the `multica agent` CLI or `POST /api/agents` — what each field is, its persisted shape, whether it is metadata-only or consumed by the daemon at claim time, which inputs are validated/rejected, how custom_env secrets are gated, and how skill binding behaves. Not for assigning issues to existing agents or for runtime task prompts."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Creating Multica agents

This is the contract for Multica's agent-creation path: what the create entry
points accept, what the server validates and rejects, how each field is
persisted, and which fields the daemon actually reads at claim time. It is
not a parameter manual — it states source-traced facts, and every claim is
backed by `file:line` in `references/creating-agents-source-map.md`.

## Quick start (read-only inspection)

These commands read state and have no side effects:

```bash
multica agent get <agent-id> --output json      # full persisted agent record
multica agent skills list <agent-id> --output json   # current skill bindings
multica agent env get <agent-id> --output json  # plaintext env (owner/admin only, agents denied)
```

`agent get` returns the agent's public management shape including `runtime_id`,
`model`, `thinking_level`, `service_tier`, value-free `custom_args` metadata,
`has_custom_env`, `custom_env_key_count`, and `skills`. It never returns
plaintext `custom_env`, any raw custom-argument value, or raw MCP/runtime config:
see the secret-field contracts below.

## Core model

An agent is a workspace-scoped row (table `agent`). Creation is a single
`POST /api/agents` (`multica agent create`). At task claim time the daemon
re-reads the agent row and assembles the runtime payload — so the persisted
fields, not the create-time output, are what the agent runs on.

Two distinct text fields, often confused:

- `description` is a catalog summary. It is stored and shown in listings; the
  daemon does NOT inject it into the agent's runtime prompt. Treat it as
  human-facing metadata only. Capped at 255 Unicode code points.
- `instructions` is the runtime behavior contract. The daemon reads it at
  claim time and ships it to the provider as the agent's durable instructions.
  Persona, responsibilities, boundaries, output and escalation rules go here,
  not in `description`.

## CLI / API entry points

Minimum create call (`--name` and `--runtime-id` are both required):

```bash
multica agent create --name <name> --runtime-id <runtime-id> \
  --description "<short catalog summary>" \
  --instructions "<runtime behavior contract>" \
  --output json
```

`runAgentCreate` builds a JSON body and posts it to `/api/agents`. It only
adds a key when its flag was provided — `description`/`instructions` on a
non-empty value, the rest (`runtime-config`, `custom-args`, `model`,
`thinking-level`, `service-tier`, `visibility`, …) on the flag being `Changed` — so omitted
flags fall through to server defaults rather than sending empty strings.

`runtime_config` and `custom_args` may carry credentials. Both create and
update offer mutually exclusive inline, stdin, and file channels:
`--runtime-config[-stdin|-file]` and `--custom-args[-stdin|-file]`. Prefer a
0600 file or stdin. Inline JSON is visible in shell history and process
listings and emits a warning. Empty file/stdin content fails closed; use `{}`
to clear runtime config or `[]` to clear custom args. A single command may
select stdin for only one JSON field, so use files for additional fields.

The HTTP body (`CreateAgentRequest`) accepts: `name`, `description`,
`instructions`, `avatar_url`, `runtime_id`, `runtime_config`, `custom_env`,
`custom_args`, `model`, `thinking_level`, `service_tier`, `visibility`,
`max_concurrent_tasks`, `mcp_config`, `skill_ids`.

## Copying an agent

`multica agent copy <source-agent-id>` forks an existing agent's portable
configuration into a brand-new agent, leaving the source untouched. It is the
CLI/headless equivalent of the web "Duplicate" action. No dedicated server API
is involved: `runAgentCopy` reads the source with `GET /api/agents/<id>`, then
POSTs a `CreateAgentRequest` — passing the source's skill ids in `skill_ids` so
the bindings attach in the SAME create transaction (unlike `agent create`, which
binds nothing). The mutation is therefore a single atomic create.

```bash
multica agent copy <source-agent-id> --name "My Agent (copy)"   # same runtime
multica agent copy <source-agent-id> --runtime-id <target> --model <model>  # cross-runtime fork
```

- Copied by default, each overridable with the matching flag: `name` (suffixed
  `" (copy)"`), `description`, `instructions`, avatar,
  `max_concurrent_tasks`, invocation permission (`permission_mode` +
  allow-list), and assigned workspace skills.
- Runtime-specific fields (`model`, `thinking_level`, `service_tier`) are copied
  ONLY when the target runtime is unchanged. `--runtime-id` selecting a
  different runtime drops them and REQUIRES `--model` (pass `--model ""` to
  accept the target runtime default), mirroring the web Duplicate clearing model
  on a runtime switch.
- Never copied: `custom_env`, `custom_args`, `mcp_config`, `runtime_config` (secret /
  machine-local; redacted or masked on read anyway). Supply fresh values with
  the same explicit flags as `agent create` (`--custom-env*`, `--custom-args*`,
  `--mcp-config*`, `--runtime-config*`), or with `agent env set` after the copy
  exists.
- `--no-skills` skips copying the source's skill bindings.

## Field contracts

| Field | Persisted as | Validated? | Consumed by |
|---|---|---|---|
| `name` | `agent.name` | required, 400 if empty | listings, runtime payload |
| `description` | `agent.description` | 400 if > 255 code points | catalog/listing only — NOT the runtime prompt |
| `instructions` | `agent.instructions` | none | daemon → provider at claim time |
| `avatar_url` | `agent.avatar_url` | none; an explicit non-empty value is preserved, while omitted/empty creates a random `emoji:<glyph>` avatar | catalog/listing UI only — NOT the runtime prompt |
| `runtime_id` | `agent.runtime_id` | required (400) + must resolve to a runtime in this workspace | selects runtime/provider |
| `model` | `agent.model` (nullable) | none beyond runtime support | daemon reads; empty = runtime default |
| `thinking_level` | `agent.thinking_level` (nullable) | provider-level enum; unknown literal → 400 | daemon; empty = runtime default |
| `service_tier` | `agent.service_tier` (nullable) | Codex-only safe token; other providers reject; exact model/tier pair checked by daemon | daemon → Codex app-server; empty = local Codex config |
| `custom_args` | `agent.custom_args` (JSON array) | JSON shape checked CLI-side; every non-empty list is value-redacted on generic reads | daemon receives raw extra CLI switches; defaults to `[]` |
| `runtime_config` | `agent.runtime_config` (JSON) | JSON shape checked CLI-side; public reads are a fail-closed projection and projected writeback preserves the stored row | daemon receives raw runtime-specific config; defaults to `{}` |
| `custom_env` | `agent.custom_env` (JSON object) | — | daemon (process env); see Env & secrets |
| `mcp_config` | `agent.mcp_config` (raw JSON) | CLI checks it is a JSON object or `null`; the server rejects `"****"` response placeholders. At create, literal `null` is dropped (no-op); at update, `null` clears the column | daemon → provider (provider-specific MCP handling); values always masked/suppressed on generic reads |
| `visibility` | `agent.visibility` | — | access control; defaults to `private`; gates who can read/route a private agent (e.g. a private squad leader) — NOT the runtime prompt |
| `max_concurrent_tasks` | `agent.max_concurrent_tasks` | — | scheduler task cap; defaults to `6` |

Defaults when omitted: `runtime_config` → `{}`, `custom_env` → `{}`,
`custom_args` → `[]`, `avatar_url` → a random `emoji:<glyph>`, `visibility` →
`private`, `max_concurrent_tasks` → `6`
(all materialized server-side before the insert). `custom_args`/`runtime_config`
are typed `[]string`/`any` and marshaled as-is — the JSON-shape rejection
happens in the CLI, not the create handler.

`thinking_level` is validated only at the provider level: fixed-catalog
providers reject an unrecognized literal, while dynamic-catalog providers such
as Codex/OpenCode accept a syntactically safe token. A value unsupported for
the chosen model is NOT rejected here — the daemon checks its local model
catalog at execution time, logs a warning, and omits the incompatible override.

Set it from the CLI with `--thinking-level` on `agent create` and `agent
update`, mirroring `--model`: the flag is a thin pass-through to the top-level
`thinking_level` field, and on update an empty string (`--thinking-level ""`)
clears it back to the runtime default. The CLI deliberately does not enumerate
the valid levels — they are runtime/model-specific (Claude currently uses
`low|medium|high|xhigh|max`; Codex values are discovered from the runtime's
model catalog). It forwards the token, the server applies the provider's
fixed-enum or safe-token gate, and the daemon performs the exact model/level
check. A runtime whose provider has no thinking concept rejects any non-empty
value with a 400.

`service_tier` is the matching first-class Codex speed control. Set it with
`--service-tier <catalog-id>` on create/update; use `--service-tier ""` on
update to clear it. The runtime model catalog owns both availability and
display copy (currently `priority`, shown as Fast). The server accepts safe
future Codex catalog IDs, while the daemon verifies the exact model/tier pair
before execution and omits a stale incompatible override. Agents without an
explicit model fail closed because the effective config.toml model is unknown.

### model vs custom_args

`model` is a first-class persisted column the daemon reads directly.
`custom_args` are raw provider CLI args. The CLI help notes that some providers
(codex app-server, openclaw) reject `--model` inside `custom_args` — but that is
documented CLI guidance, not a server-enforced invariant; nothing in the create
handler inspects `custom_args` for a model flag.

Any argument can carry credentials: split/equal `--api-key` / `--token` flags,
authorization headers, and provider-specific fields are all common. Generic
agent responses therefore hide every non-empty list and return `custom_args:
[]`, `custom_args_count`, and `custom_args_redacted: true`. The daemon claim
path still receives the raw stored argv. The empty list is non-authoritative:
management clients must explicitly replace the complete list with fresh values
or clear it. Current update clients pair a non-empty `custom_args` list with
`custom_args_intent: "replace"` and an empty list with
`custom_args_intent: "clear"`. An intent-free empty public-response replay
preserves hidden stored args for rolling compatibility; an intent-free
non-empty update is rejected once hidden args exist.

### runtime_config

`runtime_config` is persisted free-form provider JSON, so unknown keys and
arbitrary nested values are secret-bearing by default. Generic HTTP/WS/CLI/UI
responses expose only a fail-closed projection: known mode/port/TLS metadata
may remain, host/token values are masked, and unknown keys are suppressed.
`has_runtime_config`, `runtime_config_key_count`, and
`runtime_config_redacted` describe configured state without revealing values.
Daemon claims receive the raw stored JSON separately.

A projection is display-only. On update, omit the field to preserve the entire
stored config, submit a complete fresh object to replace it, or `{}` to clear
it. A current `"****"`/`_redacted` projection round-trip is treated as
"preserve"; the older exact `gateway.token: "***"` mask is supported only as a
rolling-upgrade bridge.

Use `--runtime-config-file <0600-json>` or `--runtime-config-stdin` for fresh
CLI input. Use `--custom-args-file <0600-json>` or `--custom-args-stdin` for
fresh argument lists. The inline variants remain for compatibility but are
unsafe for credentials and print a warning.

## Env & secrets

`custom_env` is secret material. The CLI offers three input channels; two keep
secrets out of shell history and the process list:

```bash
multica agent create --name <name> --runtime-id <runtime-id> --custom-env-stdin --output json
multica agent create --name <name> --runtime-id <runtime-id> --custom-env-file <0600-json> --output json
```

`--custom-env-stdin` reads the JSON object from stdin; `--custom-env-file`
reads it from a file (suggested mode 0600). The third channel,
`--custom-env <json>`, puts the value on the command line where shell history
and `ps` can see it — avoid it for real secrets.

Read-side facts (these are the wrong assumptions to avoid):

- Agent resources never expose plaintext `custom_env`. `agent
  list/get/create/update` and WS events return only `has_custom_env` (bool) and
  `custom_env_key_count` (int).
- Reading plaintext values requires the dedicated `GET /api/agents/{id}/env`
  endpoint (`multica agent env get`). It is gated to workspace **owner/admin**
  members, and **agent actors are denied** regardless of the backing member's
  role — a running agent cannot read another agent's secrets.
- Writing values after creation does NOT go through `agent update`. The generic
  update handler rejects any `custom_env` field with a 400 ("use PUT
  /api/agents/{id}/env"). Plaintext env writes are handled by
  `PUT /api/agents/{id}/env` (`multica agent env set`), which is owner/admin-only
  and writes an audit row. Its response is a value-free confirmation containing
  `agent_id`, final key names/count, a rolling-compatibility `custom_env` map
  whose values are all `"****"`, and added/removed/changed/preserved key lists
  — it never echoes submitted values. A submitted value of `"****"` keeps the
  existing value for that key; for a new key it is dropped. New clients also
  accept a plaintext `custom_env` map from an old server, derive the same
  metadata, and discard every value before returning or logging the response.

### mcp_config

`mcp_config` is the agent's MCP server configuration (a JSON object such as
`{"mcpServers": {…}}`). It is also secret material — MCP entries routinely embed
API tokens — and offers the same three input channels as `custom_env`, on BOTH
`agent create` and `agent update`:

```bash
multica agent create --name <name> --runtime-id <runtime-id> --mcp-config-file <0600-json> --output json
multica agent update <agent-id> --mcp-config-stdin --output json
multica agent update <agent-id> --mcp-config 'null'   # clears the config
```

`--mcp-config-stdin` / `--mcp-config-file` keep the value out of shell history
and `ps`; the inline `--mcp-config <json>` does not. The CLI requires a JSON
**object** or the literal `null`; a top-level array or primitive is rejected
client-side, and empty stdin/file input errors rather than silently clearing.

Two ways `mcp_config` differs from `custom_env`:

- **It IS settable through `agent update`.** Unlike `custom_env`, `mcp_config`
  has no dedicated audited endpoint — the generic `PUT /api/agents/{id}` accepts
  it. Tri-state per the raw request body: field omitted → no change; `null` →
  clear; object → replace.
- **Generic reads never return raw values, regardless of caller role.**
  `agent get`/`list`, create/update/archive/restore responses, WebSocket events,
  audit/event payloads, CLI, and UI all cross the same server-owned sanitizer.
  Supported stdio/remote entries are rebuilt as a useful masked shape:
  command/args/URL/env/header values become `"****"` and free-form server,
  env, and header names become stable aliases. Unknown, ambiguous, duplicate-key,
  or malformed shapes fail closed to `null`; `mcp_config_redacted` is `true`.
  Daemon claim paths read the raw database row separately, so runtime behavior
  is unchanged.

Masked shapes are display-only. Never GET then PUT them back: create/update
reject `"****"` placeholders. To manage a configured value, submit a complete
replacement assembled from fresh input with `mcp_config_intent: "replace"`,
omit the field to preserve it, or send `null` with
`mcp_config_intent: "clear"`. A legacy full-response replay preserves hidden
config, and an intent-free non-empty replacement is rejected once hidden config
exists.

Provider support is not uniform: Qwen Code accepts a managed `mcp_config` through a daemon-owned 0600 temporary JSON file passed with `--mcp-config`; it is removed when the run exits. Leave the field unset (`null`) to inherit Qwen Code native settings.

## Skill binding

Creating an agent does NOT bind any workspace skill — binding is a separate
call after the agent exists. Two distinct verbs:

- `add` is additive — it merges the given ids with existing bindings
  (`POST /api/agents/{id}/skills/add`).
- `set` is replace-all — it overwrites the entire binding list with exactly
  the given ids (`PUT /api/agents/{id}/skills`); `--skill-ids ''` clears all.

```bash
multica agent skills add <agent-id> --skill-ids <skill-id> --output json
multica agent skills list <agent-id> --output json
```

At claim time the daemon assembles the agent's skills as workspace-bound skills
FIRST, then appends the platform built-in skills. `LoadAgentSkills` loads each
bound skill's content plus its supporting files; built-in skills are embedded
at compile time and loaded from `SKILL.md` + sibling files. Both reach the
provider as skill content — which is why capability belongs in a bound skill,
not pasted into `instructions`.

## Side effects needing approval

Read-only (safe): `agent get`, `agent skills list`, `agent env get`.

State-changing (require an explicit instruction — do not run speculatively):

- `multica agent create` — inserts a new agent row.
- `multica agent copy` — inserts a new agent row (a fork of an existing agent);
  the source is left untouched.
- `multica agent skills add` / `set` — mutate bindings (`set` is destructive:
  it drops bindings not in the new list).
- `multica agent env set` — overwrites the full `custom_env` map and writes an
  audit row.

## Common wrong assumptions

- "`description` is the prompt." It is not — only `instructions` reaches the
  runtime. A rich description with empty instructions yields a named shell with
  no operating contract.
- "Create binds the agent's skills." It does not; bind explicitly afterward.
- "`agent update` can rotate env." It cannot — it 400s on `custom_env`; use the
  env endpoint.
- "`mcp_config` behaves like `custom_env` on update." It does not — `mcp_config`
  IS settable via `agent update` (`--mcp-config`), with `--mcp-config null` to
  clear; only `custom_env` is gated behind the dedicated env endpoint.
- "`agent get` shows env values." It shows only `has_custom_env` and
  `custom_env_key_count`.
- "`agent get` as an owner returns raw MCP config." It does not — all generic
  callers receive a masked structural summary or fail-closed `null`.
- "A masked MCP/custom-args/runtime-config response can be edited and saved." It cannot; it is
  a non-authoritative display shape. Explicitly replace the complete value or
  clear it.
- "An invalid `thinking_level`/`model` combo is caught at create." Only an
  unknown provider-level literal is — model-specific gaps fail at run time.
- "`set` and `add` are interchangeable for skills." `set` replaces all
  bindings; using it when you meant `add` silently removes capabilities.

## References

`references/creating-agents-source-map.md` maps every contract above to its
`file:line` on the current tree, the runtime effect, and a safe read-only
verification command.
