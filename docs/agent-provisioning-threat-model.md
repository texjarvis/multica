# Owner-granted agent provisioning

This is the trust boundary for a workspace owner letting one agent provision
specialists. It is not OS isolation, credential isolation, or a runtime
permission change. Role names and instructions do not grant authority.

Base: `fc397aef22aebb8cbd37069b0c6dc88d6e9369a0`. No migration in this change
inserts a grant. Default deny stays in force until a human workspace owner
creates one through the API below.

## Trust boundary

- The grantor is the authenticated human workspace **owner**. Admins, members,
  task tokens, and cloud-node PATs cannot create, expand, transfer, renew, or
  revoke a grant. There is no expand or renew route.
- Grantor identity comes from the auth middleware's user id after it strips
  client `X-Actor-Source`, then from the workspace member row. Body fields and
  a client `X-User-ID` are not the grantor.
- The grantee is one agent in that workspace. The grantee cannot be listed as
  a managed agent of its own grant, and the grant does not include grant
  management. Created agents do not inherit the grant.
- A provisioning call is accepted only for an `mat_` task token. Auth
  overwrites `X-User-ID`, `X-Agent-ID`, `X-Task-ID`, `X-Workspace-ID`, and
  `X-Actor-Source` from the token row. The handler re-reads that task and
  uses `originator_user_id` from the task row. A missing originator, a task
  that is not running for that agent, or an originator outside the grant
  fails closed. Agent ownership is not a substitute for the originator list.
- Cloud PATs stay on the human-only and machine-creation denials. They never
  open the grant exception.
- The route guard in `server/cmd/server/human_only_routes.go` still denies
  machine actors. It lets a task token through only for create, skill
  bind/replace, and runtime catalog read, and only when a `status=active`
  grant row exists for the stamped agent. An expired row still admits the
  request so the handler can deny it as expired and audit the attempt. A
  revoked row does not admit it. The handler locks the active row in the
  same transaction as the write and rejects expiry there. Revoke takes the
  same lock. A grant that disappears before the lock is acquired does not write.
- Audit rows are written in that transaction for grant, revoke, denied
  attempts, and successful provisioning. The audit stores ids, action,
  outcome, and a stable reason code. It does not store env, args, MCP, or
  runtime config. If the audit insert or the commit fails, the mutation
  rolls back.

## What a grant allows

With a locked, active, unexpired grant whose originator matches:

- `POST /api/agents` for non-secret fields: name, description, instructions,
  avatar, runtime, model, thinking level, service tier, visibility /
  invocation policy, max concurrent tasks, and existing `skill_ids`.
- `PUT /api/agents/{id}` for those same non-secret fields on a managed agent
  (an id listed on the grant, or an agent created under it).
- `POST /api/agents/{id}/skills/add` and `PUT /api/agents/{id}/skills` for
  skill ids on the grant.
- `POST /api/squads/{id}/members` to add a managed agent to an allowlisted
  squad with role `member` or an empty role.
- `GET /api/runtimes` returns only allowlisted runtimes, and only id, name,
  provider, mode, status, and visibility. Metadata, device info, daemon id,
  and profile id are omitted. Private-runtime ownership still applies.

The resulting invocation policy cannot exceed the grant: `private` always
fits; `workspace` or a member audience fits only when the grant policy is
`workspace`. Team targets are rejected. `max_concurrent_tasks` cannot exceed
the grant ceiling. Omitted concurrency on create still becomes the server
default of 6, so the caller must set a value inside the ceiling. The new-agent
counter increments under the grant lock.

## What stays denied

- Editing the grantee agent itself.
- Agents that are not managed by the grant.
- `custom_env`, `custom_args`, `mcp_config`, `runtime_config`, Composio
  allowlists, status, archive, restore, cancel, skill enable/remove, skill
  definition import, runtime profile/auth/binary changes, workspace
  membership, and billing.
- Squad create, update, delete, member removal, and role changes. Adding a
  human member is denied.
- Any use of the grant routes by a machine actor.

Ungranted task tokens keep today's denials, including metadata-only agent
updates (name, description, avatar). A task token that is missing its stamped
agent, task, or workspace identity never enters the grant path: metadata-only
updates stay on that same allowlist, and every other field is denied before
provider value checks. Once a grant exists, those metadata updates are also
limited to managed agents. An expired grant is still
`status=active`, so metadata updates enter the grant path and are denied as
expired. After revocation, metadata-only updates return to the ungranted
machine allowlist. Non-metadata updates stay denied.

Human owner and member flows do not enter the grant check.

## Files

- `server/migrations/244_agent_provisioning_grant.up.sql`
- `server/migrations/244_agent_provisioning_grant.down.sql`
- `server/pkg/db/queries/agent_provisioning.sql` and sqlc output
- `server/internal/handler/agent_provisioning.go`
- `server/internal/handler/agent.go`
- `server/internal/handler/skill.go`
- `server/internal/handler/squad.go`
- `server/internal/handler/runtime.go`
- `server/internal/middleware/task_actor.go`
- `server/cmd/server/human_only_routes.go`
- `server/cmd/server/router.go`
- `server/cmd/multica/cmd_capability.go`
- `server/cmd/multica/main.go`
- builtin `multica-creating-agents`, `multica-squads`, and
  `multica-runtimes-and-repos` instructions and source maps
- this document

## Rollback

Apply `244_agent_provisioning_grant.down.sql` with an explicit `DATABASE_URL`
pointed at the database you intend to change:

```
cd server && DATABASE_URL='postgres://.../your_db?sslmode=disable' go run ./cmd/migrate down
```

`make migrate-up` and `make migrate-down` both call
`scripts/ensure-postgres.sh`. From a checkout whose compose project is
`multica`, that script runs `docker compose up -d postgres` and can recreate
the live self-host database. Do not use those Make targets against this
worktree. Use `go run ./cmd/migrate` with a URL that is not the live stack.

The down migration drops the grant and audit tables. Previous agent, squad,
and runtime rows are not modified. Deploy the previous server binary so the
routes and CLI command disappear with the tables. Revoking a grant is the
runtime kill switch and does not require a schema rollback.

## Eight specialist configurations

The parent rollout (`System Admin`, `Identity Admin`, `Security`, `Research`,
`Reviewer`, `OpenAI Red Team`, `xAI Red Team`, `Support`) needs, per agent:
name, description, instructions, one runtime, one model, thinking level,
`max_concurrent_tasks` 1, visibility `workspace`, and existing workspace
skills, then membership in the existing Vision squad.

One grant can express that if the owner sets:

- both runtime ids, with models `gpt-6-astra` and `grok-4.7`
- `max_new_agents` at least 8 and `max_concurrent_tasks` at least 1
- `invocation_policy` `workspace`
- the squad id
- the workspace skill ids those names already resolve to
- the human originators who may trigger the grantee

`thinking_level` is ordinary non-secret config. An empty level means the
runtime default and does not need an allowlist entry. xAI Red Team uses that
default.

Owner-only activation, after this code is reviewed and deployed:

1. Owner creates the grant. Nothing in this change grants Vision.
2. Owner resolves skill names to workspace skill ids. Creating or importing
   skills stays human-only.
3. Owner confirms both runtimes already exist. Registering a runtime or
   changing its profile, binary, or auth stays human-only.
4. Owner updates Vision's own instructions and the squad instructions. The
   grant cannot edit the grantee or the squad object.
5. Owner sets any future `custom_env`, `mcp_config`, `custom_args`, or
   `runtime_config`. The rollout does not include those fields, and the
   grant cannot write them.
6. Identity Admin is not a provisioning hop. The owner grants the one
   team-lead agent directly.

Instructions still do not isolate secrets or production access. The new
agents run on the approved existing runtimes with those runtimes' current
permissions.
