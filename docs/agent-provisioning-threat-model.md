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
- Authorization uses the row state observed after the locks, in the same
  transaction as the write. Lock order is active `agent_provisioning_grant`
  rows `FOR UPDATE` (ordered by id when more than one is taken), then the
  target `agent` row `FOR UPDATE`. Revoke locks the grant by id and does not
  lock the agent, so the two paths cannot deadlock. A delegated write derives
  its resulting runtime, model, thinking level, service tier, and invocation
  policy from that locked agent and checks them against the locked grant.
  Two delegated partial updates that are each allowed against a stale
  snapshot cannot commit as a combination the grant forbids. If a human
  change commits first, the delegated partial update revalidates the new
  locked state and is denied when that pair is outside the grant.
- A human update that sets `runtime_id`, `model`, `thinking_level`, or
  `service_tier` on a managed agent takes the same lock order so it cannot
  interleave with a delegated write. The grant allowlist does not apply to
  that human update, including when the grant is expired but still
  `status=active`. Existing human authorization is unchanged, and the owner
  does not revoke the grant before editing. The human edit does not revoke
  or expand the grant. Metadata-only human updates do not take the grant
  lock. Human successes are not audited. A later authorized human edit may
  leave a runtime/model pair the grant does not allow; that pair is the
  owner's edit, not a delegated escape. A following delegated write is still
  checked against the grant and the new locked state.
- Skill bind/replace and squad-member add re-lock the target agent inside
  the grant transaction after the grant lock and before the managed-agent
  check. Creating a grant re-reads the agent, runtimes, skills, managed
  agents, squads, and originators inside the insert transaction.
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

Human owner and member flows do not borrow the grant to act as the grantee,
and the grant does not reduce the authority those humans already have.
A human who can manage the agent can change its runtime or model while a
grant is active or expired, without revoking the grant first. Thinking
level and service tier stay validated against the target runtime's provider.
Delegated runtime and model changes stay on the grant allowlist.

## Files

- `server/migrations/244_agent_provisioning_grant.up.sql` creates the tables
  without foreign keys. Primary keys and secondary indexes are
  `CREATE [UNIQUE] INDEX CONCURRENTLY` in `246`–`254`, each file one
  statement, and `255` attaches the primary keys with `USING INDEX`.
- `server/migrations/245_agent_provisioning_drop_foreign_keys.up.sql` drops
  foreign keys left by an earlier 244 so an already-migrated database matches
  a fresh install.
- `server/pkg/db/queries/workspace.sql` deletes a workspace's grant children
  and grant rows in `DeleteWorkspace` and leaves `agent_provisioning_audit`.
- `server/pkg/db/queries/agent_provisioning.sql` and sqlc output
- `scripts/ensure-postgres.sh` refuses to start the live Compose project.
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

Production rollback revokes every active grant, then rolls the server and CLI
binaries back. The audit table and the grant tables stay. `244` down and
`245` down are `SELECT 1` so a migrate down does not drop
`agent_provisioning_audit` or the grant tables. Index downs drop only the
indexes those files created.

Apply the forward migrations, including `245` (drop foreign keys), before
starting the binary that deletes grant rows from `DeleteWorkspace`. While
`agent_provisioning_grant_id` still has `ON DELETE CASCADE`, deleting the
grant or the workspace deletes the audit trail with it.

Run the migrator with an explicit `DATABASE_URL` for the database you intend
to change:

```
cd server && DATABASE_URL='postgres://.../your_db?sslmode=disable' go run ./cmd/migrate up
```

`make migrate-up` and `make migrate-down` call `scripts/ensure-postgres.sh`.
That script does not run `docker compose` unless
`MULTICA_ISOLATED_POSTGRES=1`, `COMPOSE_PROJECT_NAME` is set to a name other
than `multica`, and `COMPOSE_FILE` is an explicit file whose name does not
refer to the self-host stack. A localhost `DATABASE_URL` is checked with
`pg_isready` and does not start a container. Without either an external URL
or that isolated opt-in, the script exits non-zero.

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
