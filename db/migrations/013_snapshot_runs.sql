-- 013_snapshot_runs.sql
--
-- One row per scheduled snapshot *attempt*, including the ones that did not run.
--
-- Scheduling moves from the runner's in-memory cron to the control plane. The
-- runner's version could not answer the question that matters — "did the 03:00
-- run happen?" — because a schedule that never fires emits nothing, and a cron
-- living in a process that restarts has no memory of what it owed you.
--
-- The unique key is the whole mechanism. scheduled_at is the NOMINAL fire time
-- with no jitter applied: jitter must never enter the key, or a restart that
-- recomputes it mints a second key for the same logical run and the snapshot
-- fires twice. Inserting the row is therefore the claim on the run — a duplicate
-- insert loses harmlessly rather than needing a lock.

CREATE TABLE IF NOT EXISTS database_snapshot_runs (
    id                   INT AUTO_INCREMENT PRIMARY KEY,
    database_instance_id INT         NOT NULL,
    -- Nominal, un-jittered, UTC.
    scheduled_at         DATETIME    NOT NULL,
    -- claimed | running | completed | failed | skipped
    status               VARCHAR(20) NOT NULL DEFAULT 'claimed',
    -- Why a run did not happen, when status is skipped. A skip is recorded as a
    -- row rather than a log line: "my backups quietly stopped" is a genre of
    -- incident precisely because skips were only ever logged.
    skip_reason          VARCHAR(255) DEFAULT NULL,
    snapshot_id          INT          DEFAULT NULL,
    dispatched_at        DATETIME     DEFAULT NULL,
    finished_at          DATETIME     DEFAULT NULL,
    inserted_at          TIMESTAMP    NOT NULL DEFAULT current_timestamp(),
    updated_at           TIMESTAMP    NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
    UNIQUE KEY idx_snapshot_run_slot (database_instance_id, scheduled_at),
    KEY idx_snapshot_run_instance (database_instance_id, scheduled_at DESC)
);
