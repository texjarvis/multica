-- Single-statement concurrent build. IF NOT EXISTS keeps an already-migrated
-- database, which created this index inside 244, able to skip it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_provisioning_audit_grant_idx
    ON agent_provisioning_audit (grant_id, created_at);
