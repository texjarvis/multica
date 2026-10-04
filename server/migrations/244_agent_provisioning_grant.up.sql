-- Owner-granted, workspace-and-agent scoped provisioning. No rows are inserted
-- here. Machine actors stay denied until a human owner creates a grant.
-- Relationship checks and cleanup live in the handlers. Indexes are later
-- single-statement CREATE INDEX CONCURRENTLY migrations.

CREATE TABLE IF NOT EXISTS agent_provisioning_grant (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    granted_by uuid NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    expires_at timestamptz,
    revoked_at timestamptz,
    revoked_by uuid,
    max_new_agents integer NOT NULL CHECK (max_new_agents >= 0 AND max_new_agents <= 100),
    new_agents_created integer NOT NULL DEFAULT 0 CHECK (new_agents_created >= 0),
    max_concurrent_tasks integer NOT NULL CHECK (max_concurrent_tasks >= 1 AND max_concurrent_tasks <= 100),
    invocation_policy text NOT NULL CHECK (invocation_policy IN ('private', 'workspace')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_provisioning_grant_revocation_ck CHECK (
        (status = 'active' AND revoked_at IS NULL AND revoked_by IS NULL)
        OR (status = 'revoked' AND revoked_at IS NOT NULL AND revoked_by IS NOT NULL)
    ),
    CONSTRAINT agent_provisioning_grant_count_ck CHECK (new_agents_created <= max_new_agents)
);

CREATE TABLE IF NOT EXISTS agent_provisioning_grant_runtime (
    grant_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    model text NOT NULL DEFAULT '',
    CONSTRAINT agent_provisioning_grant_runtime_model_len CHECK (char_length(model) <= 200)
);

CREATE TABLE IF NOT EXISTS agent_provisioning_grant_skill (
    grant_id uuid NOT NULL,
    skill_id uuid NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_provisioning_grant_managed_agent (
    grant_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('allowlist', 'created'))
);

CREATE TABLE IF NOT EXISTS agent_provisioning_grant_squad (
    grant_id uuid NOT NULL,
    squad_id uuid NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_provisioning_grant_originator (
    grant_id uuid NOT NULL,
    user_id uuid NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_provisioning_audit (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL,
    grant_id uuid,
    actor_type text NOT NULL,
    actor_id uuid,
    task_id uuid,
    originator_user_id uuid,
    action text NOT NULL,
    target_type text NOT NULL DEFAULT '',
    target_id text NOT NULL DEFAULT '',
    outcome text NOT NULL CHECK (outcome IN ('success', 'denied')),
    reason text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_provisioning_audit_reason_len CHECK (char_length(reason) <= 500)
);
