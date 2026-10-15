-- 021_stack_secret_env_keys.sql
--
-- Names the keys in stacks.env_vars whose values are secret: a JSON array, e.g.
-- ["DB_DSN","JWT_KEY"]. The API returns those values as "***" to non-admins and
-- keeps the stored value when "***" is saved back. Deploys read env_vars
-- directly, so the runner still receives real values.
--
-- Additive and nullable: existing rows mean "no secrets declared", which is
-- today's behaviour.

ALTER TABLE stacks
    ADD COLUMN IF NOT EXISTS secret_env_keys TEXT NULL AFTER env_vars;
