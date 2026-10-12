-- Drops sso_sessions: one row per user, never refreshed, read only by the
-- introspection checkpoint that oidc_sessions (go-oidc-session) replaced.
-- Nothing reads or writes it. Re-runnable.
DROP TABLE IF EXISTS sso_sessions;
