-- Dropping the primary key also drops the index it owns. Later index
-- downs then no-op. The audit table itself stays.
ALTER TABLE agent_provisioning_grant DROP CONSTRAINT IF EXISTS agent_provisioning_grant_pkey;
ALTER TABLE agent_provisioning_grant_runtime DROP CONSTRAINT IF EXISTS agent_provisioning_grant_runtime_pkey;
ALTER TABLE agent_provisioning_grant_skill DROP CONSTRAINT IF EXISTS agent_provisioning_grant_skill_pkey;
ALTER TABLE agent_provisioning_grant_managed_agent DROP CONSTRAINT IF EXISTS agent_provisioning_grant_managed_agent_pkey;
ALTER TABLE agent_provisioning_grant_squad DROP CONSTRAINT IF EXISTS agent_provisioning_grant_squad_pkey;
ALTER TABLE agent_provisioning_grant_originator DROP CONSTRAINT IF EXISTS agent_provisioning_grant_originator_pkey;
ALTER TABLE agent_provisioning_audit DROP CONSTRAINT IF EXISTS agent_provisioning_audit_pkey;
