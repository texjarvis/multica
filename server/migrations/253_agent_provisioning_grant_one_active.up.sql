-- Single-statement concurrent build. IF NOT EXISTS keeps an already-migrated
-- database, which created this index inside 244, able to skip it.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_provisioning_grant_one_active
    ON agent_provisioning_grant (workspace_id, agent_id) WHERE status = 'active';
