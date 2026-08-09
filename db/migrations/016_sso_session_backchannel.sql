-- 016_sso_session_backchannel.sql
--
-- Back-channel logout (OIDC Back-Channel Logout 1.0) finds sessions by what the
-- logout token names: a `sid`, or failing that a `sub`. sso_sessions could be
-- looked up by neither — its only key is user_id.
--
-- ⚠️ NEITHER COLUMN CAN BE BACKFILLED. `subject` is at least derivable from the
-- users table, but `sid` exists only inside the id_token of the login that
-- created the session, and that token is gone by the time anything else runs.
-- Every session established before this migration is therefore unreachable by a
-- session-scoped logout for the rest of its life. That is the argument for
-- adding the columns now rather than when the receiver is finished — the cost of
-- waiting is paid by sessions, not by code.
--
-- ⚠️ `sid` STAYS NULL UNTIL sso.issuer_url IS SET. Without an issuer this service
-- uses the OAuth2 adapter, which has no id_token and therefore no `sid` to
-- store. The column existing is not the same as it being populated.
--
-- Both are NULLABLE on purpose and neither absence is an error: the receiver
-- treats "no matching session" as a normal outcome.

ALTER TABLE sso_sessions
    ADD COLUMN IF NOT EXISTS provider VARCHAR(64) NOT NULL DEFAULT 'sso';

ALTER TABLE sso_sessions
    ADD COLUMN IF NOT EXISTS subject VARCHAR(255) NULL;

ALTER TABLE sso_sessions
    ADD COLUMN IF NOT EXISTS sid VARCHAR(64) NULL;

-- Lookups are always scoped by provider as well as the identifier. `sid` and
-- `sub` are unique only WITHIN an issuer, so an unscoped match would let one
-- identity provider end another's sessions.
ALTER TABLE sso_sessions
    ADD INDEX IF NOT EXISTS idx_sso_sessions_provider_sid (provider, sid);

ALTER TABLE sso_sessions
    ADD INDEX IF NOT EXISTS idx_sso_sessions_provider_subject (provider, subject);
