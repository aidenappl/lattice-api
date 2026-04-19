-- Migration 010: remove duplicate rows from container_logs
--
-- Duplicate rows may have been inserted by runner reconnects before the
-- `recorded_at` timestamp was tracked per-line. This removes all but the
-- lowest-id copy of rows that share the same (container_id, recorded_at,
-- message) values so that migration 011 can safely add a unique index.
--
-- NULL-safe: MariaDB GROUP BY treats NULL values as equal, so rows where
-- container_id IS NULL are correctly grouped together.

DELETE cl
FROM container_logs AS cl
INNER JOIN (
    -- For every unique tuple, keep only the earliest (lowest) id.
    SELECT MIN(id) AS keep_id, container_id, recorded_at, message
    FROM container_logs
    GROUP BY container_id, recorded_at, message
    HAVING COUNT(*) > 1
) AS dups
  ON  cl.container_id  <=> dups.container_id
  AND cl.recorded_at       = dups.recorded_at
  AND cl.message           = dups.message
  AND cl.id               != dups.keep_id;
