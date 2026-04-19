-- Migration 010: improve container_logs for deduplication
--
-- 1. Upgrade recorded_at to DATETIME(6) so Docker's nanosecond timestamps
--    can be stored with microsecond precision (MariaDB max).
-- 2. Add a unique index on (container_id, recorded_at, message) so that
--    INSERT IGNORE will silently discard runner-restart replays of the same
--    log line without error.
--
-- The prefix length of 191 on `message` keeps the index within MariaDB's
-- 767-byte limit for utf8mb4 columns (191 × 4 = 764 bytes).

ALTER TABLE container_logs
  MODIFY COLUMN recorded_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6);

ALTER TABLE container_logs
  ADD UNIQUE KEY uq_container_log_dedup (container_id, recorded_at, message(191));
