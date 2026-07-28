# Creating agents — source map

Evidence layer for `SKILL.md`. Every contract maps to `file:line` on the
current tree, the runtime effect, and a safe read-only check. Line numbers were
re-derived against this tree — re-derive again if the files move, the
surrounding context (not the number) is the anchor.

## Verification

```bash
# Conformance eval for this skill (and the shared template invariants):
go test ./internal/service -run TestCreatingAgentsSkillCoversAgentCreationContracts
go test ./internal/service -run TestBuiltinSkillsConformToTemplate
```

## CLI entry points — `server/cmd/multica/cmd_agent.go`

| Contract | Line | Behavior | Safe check |
|---|---|---|---|
| Create flags: `name`, `description`, `instructions`, `runtime-id` | 160–163 | Registered create flags; `name`/`runtime-id` enforced in `runAgentCreate` | `multica agent create --help` |
| Secret-safe runtime/argument inputs: `runtime-config[-stdin|-file]`, `custom-args[-stdin|-file]`; plus `model`, `thinking-level`, `service-tier` | 164–174 | Runtime config and custom args use mutually exclusive inline/stdin/file channels; inline warns, empty safe input fails closed, and a command can consume stdin for only one JSON field. `model` help prefers the dedicated flag over custom args; thinking and Codex service-tier values are catalog-owned pass-throughs | `multica agent create --help` |
| Secret-safe env input: `custom-env`, `custom-env-stdin`, `custom-env-file` | 169–171 | `--custom-env` warns about shell history / `ps`; stdin and file modes keep secrets off the command line; mutually exclusive | `multica agent create --help` |
| Secret-safe MCP input: `mcp-config`, `mcp-config-stdin`, `mcp-config-file` (create) | 172–174 | Same three-channel pattern as `custom-env`; `--mcp-config` warns about shell history / `ps`; value must be a JSON object or `null` | `multica agent create --help` |
| MCP flags on `agent update` | 200–202 | Same three channels on update; an object sends explicit replace intent and `--mcp-config null` sends explicit clear intent. Unlike `custom_env`, `mcp_config` IS settable via update | `multica agent update --help` |
| `thinking-level` / `service-tier` flags on `agent update` | 189–190 | Thin pass-throughs; an explicit empty string clears the saved override and restores the runtime/local Codex default | `multica agent update --help` |
| `runAgentCreate` builds body + `POST /api/agents` | 624–717 | Only sets a body key when the flag `Changed`; posts to `/api/agents` (705); response passes the CLI fail-closed sanitizer (708) | read 624–717 |
| Body assembly: description/instructions/runtime-config/custom-args/custom-env/mcp-config/model/thinking-level/service-tier | 643–690 | `model`, `thinking_level`, and `service_tier` are `Changed`-gated pass-throughs; omitted flags are not sent | read the `runAgentCreate` body assembly |
| `runAgentUpdate` hidden-field intents | 719–813 | `--custom-args` sends `custom_args_intent=replace|clear` at 750–762; MCP input sends `mcp_config_intent=replace|clear` at 791–800. `custom_env` is intentionally not a flag here; response is sanitized before output | read the `runAgentUpdate` body assembly |
| `parseMcpConfig` / `resolveMcpConfig` helpers | 1325, 1353 | Validator (object-or-`null`, content-free errors) + three-channel resolver, mirroring `parseCustomEnv`/`resolveCustomEnv` | read 1325–1415 |
| `agent skills set` = replace-all | 916 | `PUT /api/agents/{id}/skills` (934); `--skill-ids ''` clears all (922–925) | `multica agent skills set --help` |
| `agent skills add` = additive | 941 | `POST /api/agents/{id}/skills/add` (962); requires ≥1 id (947–952) | `multica agent skills add --help` |
| `agent skills list` | 884 | reads bindings, no side effect | `multica agent skills list --help` |
| `agent env get` | 1124 | `GET /api/agents/{id}/env` (1134) | `multica agent env get --help` |
| `agent env set` | 1160 | `PUT /api/agents/{id}/env` with full `custom_env` map; consumes a typed value-free key/count/change confirmation (1179–1189) | `multica agent env set --help` |

## Copy command — `server/cmd/multica/cmd_agent_copy.go`

| Contract | Line | Behavior | Safe check |
|---|---|---|---|
| `agentCopyCmd` (`copy <source-agent-id>`) + flag registrar | 21, 47, 54 | Own file with its own `init()` so `cmd_agent.go` line refs stay stable; `registerAgentCopyFlags` is shared with the tests | `multica agent copy --help` |
| Reads source via `GET /api/agents/<id>` | 85–98 | Composes over existing endpoints — no dedicated copy API — then applies the CLI response sanitizer | read `runAgentCopy` |
| Same-runtime vs cross-runtime rule | 105–145 | `sameRuntime` copies `model`/`thinking_level`/`service_tier`; a different `--runtime-id` drops them and requires `--model` (empty allowed) | `multica agent copy --help` |
| Skills copied in the create transaction | 226–239 | Source skill ids sent as `skill_ids`, bound in the same `POST /api/agents` tx; `--no-skills` opts out | read `runAgentCopy` |
| Secrets never copied | 150–266 | `custom_args`/`custom_env`/`mcp_config`/`runtime_config` are set only from explicit flags, never read from the source | `multica agent copy --help` |

Note: the CLI no longer exposes `--from-template`. The agent-template backend
still exists (registry `server/internal/agenttmpl/`, handler `agent_template.go`,
routes `GET /api/agent-templates` and `POST /api/agents/from-template`, plus the
`packages/core` client/query wrappers) but is currently orphaned plumbing with no
live caller: the removed CLI flag was its only non-test consumer, and onboarding
does NOT use it — `packages/views/onboarding/steps/step-agent.tsx` builds four
hardcoded local presets (i18n-resolved) and creates via plain `POST /api/agents`
(`createAgent`), never `POST /api/agents/from-template`. Do not treat the template
API as a supported agent-creation path. This skill teaches manual `agent create`
only.

## Create handler — `server/internal/handler/agent.go`

| Contract | Line | Behavior |
|---|---|---|
| `maxAgentDescriptionLength = 255` | 36 | Cap is 255 **Unicode code points** (comment: counted via `utf8.RuneCountInString`, matches Postgres `char_length`) |
| `AgentResponse` secret-bearing fields are projected | 38–112 | `runtime_config` is projection + metadata (47–53), `custom_args` is empty + count/redacted (54–60), and `custom_env` exposes only count metadata (62–71) |
| Server-owned generic response boundary | 126–658 | `agentToResponse` suppresses every non-empty custom-argument list and runs runtime/MCP config through validating fail-closed projectors (225 and 330). This mapper feeds HTTP/UI/events/WS; daemon claims use raw fields separately |
| Hidden mutation-intent parser | 126–177 | Accepts only `replace` / `clear`; malformed persisted JSON fails closed as authoritative hidden data |
| Projected writeback protection | 668–734, 1578–1607, 2062–2218 | Create rejects response placeholders; update preserves a current runtime projection, bridges legacy `gateway.token:"***"`, requires explicit custom-args/MCP replace-or-clear intent for destructive writes, preserves blind legacy response replays, and rejects ambiguous non-empty legacy writes against hidden values |
| `CreateAgentRequest` fields | 1422–1460 | Includes `model`, `thinking_level`, and Codex `service_tier` alongside the profile/runtime/permission inputs |
| `name` required | 1498–1501 | 400 "name is required" |
| `description` ≤ 255 code points | 1502–1505 | `utf8.RuneCountInString(req.Description) > maxAgentDescriptionLength` → 400 |
| `runtime_id` required | 1506–1509 | `if req.RuntimeID == ""` → 400 "runtime_id is required" |
| `runtime_id` must resolve in workspace | 1517–1543 | parsed + `GetAgentRuntimeForWorkspace`; unknown → 400 "invalid runtime_id" |
| `thinking_level` provider-level validation | 1555–1565 | fixed providers use an enum, Codex/OpenCode use safe-token syntax, and per-model gaps are deferred to daemon (MUL-2339) |
| `service_tier` provider-level validation | `agent.go` create/update paths | Non-empty values are Codex-only safe tokens; exact per-model support is daemon-owned |
| Defaults: `{}` config/env, `[]` args | 1586–1599 | `RuntimeConfig`→`{}`, `CustomEnv`→`{}`, `CustomArgs`→`[]` when nil, before insert |
| `visibility` default | 1510–1512 | `if req.Visibility == "" { req.Visibility = "private" }` — access-control field, not the runtime prompt |
| `max_concurrent_tasks` default | 1513–1515 | `if req.MaxConcurrentTasks == 0 { req.MaxConcurrentTasks = 6 }` — scheduler cap |
| `mcp_config` null-skip on create | 1601–1608 | raw JSON copied through unless the body value is the literal `null`; masked placeholders are rejected |
| `mcp_config` redacted on every generic response | 126–206, 330–658 | All supported configs return only masked structure with `McpConfigRedacted=true`; malformed/unknown configs return `null` fail-closed |
| Qwen Code managed-MCP injection | `pkg/agent/qwen.go` | Non-null `mcp_config` is written to a daemon-owned 0600 temporary JSON file and passed with `--mcp-config`; the file is removed after the process exits, while `null` preserves native inheritance. |
| Random emoji avatar default | `agent_avatar.go` 11–32; `agent.go` 1583 | Omitted, empty, or whitespace-only `avatar_url` becomes a cryptographically selected `emoji:<glyph>` sentinel; explicit values are preserved. The template handler uses the same helper at `agent_template.go` 458. |
| `CreateAgent` insert params | `agent.go` create path | Persists avatar_url, runtime_config, instructions, custom_env, custom_args, model, thinking_level, service_tier, mcp_config, visibility, max_concurrent_tasks |
| `UpdateAgent` rejects `custom_env` | 2079–2089 | if `custom_env` present in body → 400 "use PUT /api/agents/{id}/env (or `multica agent env set`)" |
| `UpdateAgent` projects/preserves secret fields | 2090–2218 | runtime config projection → preserve; custom args and MCP require explicit replace/clear intent; blind/full legacy response replay preserves; ambiguous non-empty legacy input against an existing hidden value returns 409 |
| `description` ≤ 255 on update too | 2042–2047 | same cap re-checked on update |

## Runtime model/thinking discovery — `server/pkg/agent/{models,thinking}.go`

| Contract | Line | Behavior |
|---|---|---|
| Codex model-list entry point | `models.go` 94–103 | `ListModels("codex")` uses cached daemon-local discovery instead of returning the fallback catalog unconditionally |
| Codex fallback catalog | `models.go` 301–354 | Used for Codex <0.122.0 and failed/malformed discovery; includes current verified visible models plus legacy `gpt-5.3-codex`, with a separate `Thinking` catalog on every model |
| Codex discovery version gate | `thinking.go` 280, 306–337 | `codex debug models --bundled` is used only for parseable versions ≥0.122.0; unsupported versions and command/parse/empty failures return the static model + thinking fallback |
| Codex catalog projection | `thinking.go` `parseCodexModelCatalog` | Hidden models are excluded; visible model, reasoning, and `service_tiers` metadata are preserved |
| Per-model thinking validation | `thinking.go` 547–640 | `ValidateThinkingLevel` accepts only values in the explicit model's `Thinking.SupportedLevels`; an empty Codex model fails closed because its effective `config.toml` model is unknown |
| Dynamic Codex token gate | `thinking.go` 642–710 | Server persistence accepts syntactically safe Codex tokens so new catalog values do not require a Multica release; exact support remains a daemon-local per-model check |
| Per-model service-tier validation | `thinking.go` `ValidateServiceTier` | Accepts only a tier advertised for the explicit Codex model; empty model fails closed because config.toml is unknown |
| Daemon invalid-combination handling | `internal/daemon/daemon.go` 3860–3892 | Before execution, invalid `(provider, model, thinking_level)` combinations log a warning and omit the override rather than failing the task |

## Env endpoint — `server/internal/handler/agent_env.go`

| Contract | Line | Behavior |
|---|---|---|
| Separate GET and PUT DTOs | 41–72 | GET response alone carries plaintext `custom_env`; PUT confirmation contains agent id, a `custom_env` map whose values are all `"****"`, final key names/count, and change metadata |
| `authorizeAgentEnv` gate | 83 | loads agent, then applies the two checks below |
| Agent actors denied | 98–101 | `if actorType == "agent"` → 403 "agents may not access env management endpoints" (MUL-2600 impersonation guard) |
| Owner/admin only | 103 | `requireWorkspaceRole(..., "owner", "admin")` |
| Plaintext audited GET | 123–156 | successful reveal returns `AgentEnvResponse` only after the audit row is written |
| Value-free audited PUT | 171–277 | full-map update and audit commit together; `newAgentEnvUpdateResponse` masks every retained compatibility-map value. `mergeAgentEnv` at 302 preserves existing values submitted as `"****"` and drops a masked new key |

## Routes — `server/cmd/server/router.go`

| Contract | Line | Behavior |
|---|---|---|
| `GET /env` | 1275 | `h.GetAgentEnv` (plaintext read, gated) |
| `PUT /env` | 1276 | `h.UpdateAgentEnv` (full-map overwrite, gated; value-free response) |

## Claim-time injection — `server/internal/handler/daemon.go`

| Contract | Line | Behavior |
|---|---|---|
| Raw claim config boundary | 1582–1619 | `rawAgentConfigForClaim` decodes/copies persisted custom env, custom args, MCP config, and runtime config without public projection |
| Fresh agent re-read on claim | 1642–1644 | `GetAgent(task.AgentID)` — claim uses persisted fields, not create output |
| Runtime payload | 1660–1669 | Carries raw `CustomEnv`, `CustomArgs`, `McpConfig`, and `RuntimeConfig` plus model/thinking/service tier |

## Runtime profiles — `server/internal/handler/runtime_profile.go`

| Contract | Line | Behavior |
|---|---|---|
| Public response projection | 34–72 | `fixed_args` is always empty; count/redacted metadata remains |
| Daemon-only raw response | 75–103 | separate DTO decodes and returns exact `fixed_args` |
| Public list vs daemon list | 247–266, 566–589 | public list uses the redacted mapper; authenticated daemon sync uses the raw mapper |
| Fixed-argument update intent | 295–407 | explicit `fixed_args_intent=replace|clear` performs the write; an empty legacy public replay preserves stored args, while ambiguous non-empty legacy input against hidden args returns 409 |

## Runtime-profile CLI — `server/cmd/multica/cmd_runtime_profile.go`

| Contract | Line | Behavior |
|---|---|---|
| Fixed-argument flags | 92–113 | create/update accept mutually exclusive `--fixed-args`, `--fixed-args-stdin`, and `--fixed-args-file`; inline warns about argv exposure; update also exposes `--clear-fixed-args` |
| Fixed-argument create/update body | 215–330 | safe channels reject empty input; update replacement requires a fresh non-empty array and sends `fixed_args_intent=replace`; clear sends an empty array and `fixed_args_intent=clear` |

## Task service boundaries — `server/internal/service/task.go`

| Contract | Line | Behavior |
|---|---|---|
| Task-driven agent status events | 3888–3917, 4753–4767 | reconciliation and direct status updates may load a full persisted `db.Agent`, but the bus payload is rebuilt from a four-field allowlist: `id`, `workspace_id`, `status`, `updated_at` |
| `LoadAgentSkills` | 3920 | `ListAgentSkills` + per-skill `ListSkillFiles` → content + supporting files for execution |

## Built-in skills — `server/internal/service/builtin_skills.go`

| Contract | Line | Behavior |
|---|---|---|
| `go:embed builtin_skills` | 10–11 | skills embedded at compile time |
| `loadBuiltinSkill` | 45 | reads `<name>/SKILL.md` (47) + walks sibling files into `Files` (56–68) |

## Persisted columns — `server/pkg/db/generated/agent.sql.go`

| Contract | Line | Behavior |
|---|---|---|
| `CreateAgent` INSERT | generated from `queries/agent.sql` | columns include `runtime_config, runtime_id, instructions, custom_env, custom_args, mcp_config, model, thinking_level, service_tier` |
| `CreateAgentParams` | generated from `queries/agent.sql` | typed params include nullable `Model`, `ThinkingLevel`, and `ServiceTier` |
| `UpdateAgent` SET | generated from `queries/agent.sql` | COALESCE updates include model/thinking/service tier; dedicated clear queries restore each nullable override |
| `UpdateAgentCustomEnv` (called by the `UpdateAgentEnv` handler) | 5545–5562 | `SET custom_env = $2` — the only write path for env values |
