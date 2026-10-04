-- name: CreateProvisioningGrant :one
INSERT INTO agent_provisioning_grant (
    workspace_id, agent_id, granted_by, expires_at,
    max_new_agents, max_concurrent_tasks, invocation_policy
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: InsertProvisioningGrantRuntime :exec
INSERT INTO agent_provisioning_grant_runtime (grant_id, runtime_id, model)
VALUES ($1, $2, $3);

-- name: InsertProvisioningGrantSkill :exec
INSERT INTO agent_provisioning_grant_skill (grant_id, skill_id)
VALUES ($1, $2);

-- name: InsertProvisioningManagedAgent :exec
INSERT INTO agent_provisioning_grant_managed_agent (grant_id, agent_id, source)
VALUES ($1, $2, $3);

-- name: InsertProvisioningGrantSquad :exec
INSERT INTO agent_provisioning_grant_squad (grant_id, squad_id)
VALUES ($1, $2);

-- name: InsertProvisioningGrantOriginator :exec
INSERT INTO agent_provisioning_grant_originator (grant_id, user_id)
VALUES ($1, $2);

-- name: LockActiveProvisioningGrant :one
SELECT * FROM agent_provisioning_grant
WHERE workspace_id = $1 AND agent_id = $2 AND status = 'active'
FOR UPDATE;

-- name: ShareActiveProvisioningGrant :one
SELECT * FROM agent_provisioning_grant
WHERE workspace_id = $1 AND agent_id = $2 AND status = 'active'
FOR SHARE;

-- name: ActiveProvisioningGrantExists :one
-- Status active includes an expired row. Callers use this only as a coarse
-- admission probe; the handler denies expiry under the row lock and audits it.
-- Revoked rows do not match.
SELECT EXISTS (
    SELECT 1 FROM agent_provisioning_grant
    WHERE workspace_id = $1 AND agent_id = $2 AND status = 'active'
) AS ok;

-- name: IncrementProvisioningGrantCreateCount :one
UPDATE agent_provisioning_grant
SET new_agents_created = new_agents_created + 1,
    updated_at = now()
WHERE id = $1
  AND status = 'active'
  AND new_agents_created < max_new_agents
  AND (expires_at IS NULL OR expires_at > now())
RETURNING *;

-- name: ProvisioningRuntimeAllowed :one
SELECT EXISTS (
    SELECT 1 FROM agent_provisioning_grant_runtime
    WHERE grant_id = $1 AND runtime_id = $2 AND model = $3
) AS ok;

-- name: ProvisioningSkillAllowed :one
SELECT EXISTS (
    SELECT 1 FROM agent_provisioning_grant_skill
    WHERE grant_id = $1 AND skill_id = $2
) AS ok;

-- name: ProvisioningSquadAllowed :one
SELECT EXISTS (
    SELECT 1 FROM agent_provisioning_grant_squad
    WHERE grant_id = $1 AND squad_id = $2
) AS ok;

-- name: ProvisioningOriginatorAllowed :one
SELECT EXISTS (
    SELECT 1 FROM agent_provisioning_grant_originator
    WHERE grant_id = $1 AND user_id = $2
) AS ok;

-- name: ProvisioningManagedAgentSource :one
SELECT source FROM agent_provisioning_grant_managed_agent
WHERE grant_id = $1 AND agent_id = $2;

-- name: ListProvisioningGrantRuntimes :many
SELECT runtime_id, model FROM agent_provisioning_grant_runtime
WHERE grant_id = $1
ORDER BY runtime_id, model;

-- name: ListProvisioningGrantSkills :many
SELECT skill_id FROM agent_provisioning_grant_skill
WHERE grant_id = $1
ORDER BY skill_id;

-- name: ListProvisioningGrantManagedAgents :many
SELECT agent_id, source FROM agent_provisioning_grant_managed_agent
WHERE grant_id = $1
ORDER BY agent_id;

-- name: ListProvisioningGrantSquads :many
SELECT squad_id FROM agent_provisioning_grant_squad
WHERE grant_id = $1
ORDER BY squad_id;

-- name: ListProvisioningGrantOriginators :many
SELECT user_id FROM agent_provisioning_grant_originator
WHERE grant_id = $1
ORDER BY user_id;

-- name: ListProvisioningGrantRuntimeIDs :many
SELECT DISTINCT runtime_id FROM agent_provisioning_grant_runtime
WHERE grant_id = $1
ORDER BY runtime_id;

-- name: GetLatestProvisioningGrantForAgent :one
SELECT * FROM agent_provisioning_grant
WHERE workspace_id = $1 AND agent_id = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: GetProvisioningGrantForWorkspace :one
SELECT * FROM agent_provisioning_grant
WHERE id = $1 AND workspace_id = $2;

-- name: LockProvisioningGrantByID :one
SELECT * FROM agent_provisioning_grant
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: RevokeProvisioningGrant :one
UPDATE agent_provisioning_grant
SET status = 'revoked',
    revoked_at = now(),
    revoked_by = $2,
    updated_at = now()
WHERE id = $1 AND workspace_id = $3 AND status = 'active'
RETURNING *;

-- name: InsertProvisioningAudit :one
INSERT INTO agent_provisioning_audit (
    workspace_id, grant_id, actor_type, actor_id, task_id, originator_user_id,
    action, target_type, target_id, outcome, reason
) VALUES (
    $1, sqlc.narg('grant_id'), $2, sqlc.narg('actor_id'), sqlc.narg('task_id'),
    sqlc.narg('originator_user_id'), $3, $4, $5, $6, $7
)
RETURNING id;
