-- 005: Add compose_yaml to stacks for storing the original compose file
ALTER TABLE stacks ADD COLUMN IF NOT EXISTS compose_yaml TEXT DEFAULT NULL AFTER env_vars;
