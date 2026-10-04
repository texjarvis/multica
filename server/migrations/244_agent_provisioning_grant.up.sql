-- Owner-granted, workspace-and-agent scoped provisioning. No rows are inserted
-- here. Machine actors stay denied until a human owner creates a grant.

CREATE TABLE agent_provisioning_grant (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    granted_by uuid NOT NULL REFERENCES "user"(id),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    expires_at timestamptz,
    revoked_at timestamptz,
    revoked_by uuid REFERENCES "user"(id),
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

CREATE UNIQUE INDEX agent_provisioning_grant_one_active
    ON agent_provisioning_grant (workspace_id, agent_id)
    WHERE status = 'active';

CREATE TABLE agent_provisioning_grant_runtime (
    grant_id uuid NOT NULL REFERENCES agent_provisioning_grant(id) ON DELETE CASCADE,
    runtime_id uuid NOT NULL,
    model text NOT NULL DEFAULT '',
    PRIMARY KEY (grant_id, runtime_id, model),
    CONSTRAINT agent_provisioning_grant_runtime_model_len CHECK (char_length(model) <= 200)
);

CREATE TABLE agent_provisioning_grant_skill (
    grant_id uuid NOT NULL REFERENCES agent_provisioning_grant(id) ON DELETE CASCADE,
    skill_id uuid NOT NULL,
    PRIMARY KEY (grant_id, skill_id)
);

CREATE TABLE agent_provisioning_grant_managed_agent (
    grant_id uuid NOT NULL REFERENCES agent_provisioning_grant(id) ON DELETE CASCADE,
    agent_id uuid NOT NULL,
    source text NOT NULL CHECK (source IN ('allowlist', 'created')),
    PRIMARY KEY (grant_id, agent_id)
);

CREATE TABLE agent_provisioning_grant_squad (
    grant_id uuid NOT NULL REFERENCES agent_provisioning_grant(id) ON DELETE CASCADE,
    squad_id uuid NOT NULL,
    PRIMARY KEY (grant_id, squad_id)
);

CREATE TABLE agent_provisioning_grant_originator (
    grant_id uuid NOT NULL REFERENCES agent_provisioning_grant(id) ON DELETE CASCADE,
    user_id uuid NOT NULL,
    PRIMARY KEY (grant_id, user_id)
);

CREATE TABLE agent_provisioning_audit (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    grant_id uuid REFERENCES agent_provisioning_grant(id) ON DELETE CASCADE,
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

CREATE INDEX agent_provisioning_audit_grant_idx
    ON agent_provisioning_audit (grant_id, created_at);
