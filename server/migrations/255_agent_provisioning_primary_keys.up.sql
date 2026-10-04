-- Attach primary keys to the unique indexes from 246-252. The partial
-- unique index and the audit grant index are not primary keys.
-- One DO block is one statement; these are not concurrent index builds.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_grant_pkey'
          AND conrelid = 'agent_provisioning_grant'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_grant
            ADD CONSTRAINT agent_provisioning_grant_pkey
            PRIMARY KEY USING INDEX agent_provisioning_grant_pkey;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_grant_runtime_pkey'
          AND conrelid = 'agent_provisioning_grant_runtime'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_grant_runtime
            ADD CONSTRAINT agent_provisioning_grant_runtime_pkey
            PRIMARY KEY USING INDEX agent_provisioning_grant_runtime_pkey;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_grant_skill_pkey'
          AND conrelid = 'agent_provisioning_grant_skill'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_grant_skill
            ADD CONSTRAINT agent_provisioning_grant_skill_pkey
            PRIMARY KEY USING INDEX agent_provisioning_grant_skill_pkey;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_grant_managed_agent_pkey'
          AND conrelid = 'agent_provisioning_grant_managed_agent'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_grant_managed_agent
            ADD CONSTRAINT agent_provisioning_grant_managed_agent_pkey
            PRIMARY KEY USING INDEX agent_provisioning_grant_managed_agent_pkey;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_grant_squad_pkey'
          AND conrelid = 'agent_provisioning_grant_squad'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_grant_squad
            ADD CONSTRAINT agent_provisioning_grant_squad_pkey
            PRIMARY KEY USING INDEX agent_provisioning_grant_squad_pkey;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_grant_originator_pkey'
          AND conrelid = 'agent_provisioning_grant_originator'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_grant_originator
            ADD CONSTRAINT agent_provisioning_grant_originator_pkey
            PRIMARY KEY USING INDEX agent_provisioning_grant_originator_pkey;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'agent_provisioning_audit_pkey'
          AND conrelid = 'agent_provisioning_audit'::regclass
    ) THEN
        ALTER TABLE agent_provisioning_audit
            ADD CONSTRAINT agent_provisioning_audit_pkey
            PRIMARY KEY USING INDEX agent_provisioning_audit_pkey;
    END IF;
END $$;
