CREATE TABLE IF NOT EXISTS sso_sessions (
    user_id             BIGINT       NOT NULL PRIMARY KEY,
    access_token        TEXT         NOT NULL,
    refresh_token       TEXT         NOT NULL,
    last_checked_at     DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inserted_at         DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT fk_lattice_sso_sessions_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    INDEX idx_lattice_sso_sessions_last_checked (last_checked_at)
);
