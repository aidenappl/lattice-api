-- 015_snapshot_replicas.sql
--
-- A snapshot becomes one logical artifact with one row per destination copy,
-- plus an optional mirror destination per instance.
--
-- One primary + one mirror is exactly what 3-2-1 needs and no more: general
-- fan-out to N destinations is a different, larger feature, and building it
-- speculatively would add failure modes nobody asked for.
--
-- Per-replica rows rather than a boolean on the snapshot, because the states are
-- genuinely independent: the primary can succeed while the mirror fails, and
-- that must degrade posture without failing the backup.

ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS mirror_backup_destination_id INT DEFAULT NULL;

CREATE TABLE IF NOT EXISTS database_snapshot_replicas (
    id                    INT AUTO_INCREMENT PRIMARY KEY,
    snapshot_id           INT         NOT NULL,
    backup_destination_id INT         NOT NULL,
    -- primary | mirror
    role                  VARCHAR(16) NOT NULL DEFAULT 'primary',
    -- pending | completed | failed
    status                VARCHAR(16) NOT NULL DEFAULT 'pending',
    size_bytes            BIGINT       DEFAULT NULL,
    error_message         TEXT         DEFAULT NULL,
    completed_at          DATETIME     DEFAULT NULL,
    inserted_at           TIMESTAMP   NOT NULL DEFAULT current_timestamp(),
    updated_at            TIMESTAMP   NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
    UNIQUE KEY idx_replica_snapshot_dest (snapshot_id, backup_destination_id),
    KEY idx_replica_snapshot (snapshot_id)
);
