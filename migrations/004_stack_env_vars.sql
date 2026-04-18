-- 004: Add env_vars to stacks for stack-level environment variables
ALTER TABLE stacks ADD COLUMN IF NOT EXISTS env_vars TEXT DEFAULT NULL AFTER auto_deploy;
