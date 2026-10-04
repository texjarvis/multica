-- Leave the foreign keys dropped. Re-adding them would restore ON DELETE CASCADE
-- from a grant or workspace delete onto agent_provisioning_audit.
SELECT 1;
