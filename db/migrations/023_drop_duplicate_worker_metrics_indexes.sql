-- 023_drop_duplicate_worker_metrics_indexes.sql
--
-- worker_metrics had six secondary indexes, two of them duplicates:
--   idx_metrics_recorded (recorded_at)  — identical to idx_wm_recorded
--   idx_metrics_worker   (worker_id)    — a prefix of idx_wm_worker_recorded
-- Every metrics insert (one per worker every 10 s) updated both for nothing.
--
-- idx_metrics_worker currently backs fk_metrics_worker. The two CREATEs make
-- sure the replacement indexes exist first (both come from the legacy
-- migrate() block, which ignores failures), so the FK always keeps a usable
-- index and the DROP can't fail with error 1553. All four are no-ops on a
-- re-run.

CREATE INDEX IF NOT EXISTS idx_wm_worker_recorded ON worker_metrics (worker_id, recorded_at);
CREATE INDEX IF NOT EXISTS idx_wm_recorded ON worker_metrics (recorded_at);
DROP INDEX IF EXISTS idx_metrics_recorded ON worker_metrics;
DROP INDEX IF EXISTS idx_metrics_worker ON worker_metrics;
