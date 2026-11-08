-- 026_log_scope_indexes.sql
--
-- APL-119: GET /admin/containers/{id}/logs and /lifecycle took ~6s for a busy
-- container (nexus-api) and timed lattice-web out. The lookup matches
-- container_id = ? OR (container_id IS NULL AND container_name = ?) newest
-- first; query.logScope now runs each side as its own ordered, limited SELECT
-- (UNION ALL), and these indexes let each side read limit rows backwards off an
-- index instead of sorting every row the container has logged.
--
-- Additive and idempotent. InnoDB builds secondary indexes online (no table
-- lock), so the logs tables keep taking inserts while this runs.

ALTER TABLE container_logs
    ADD INDEX IF NOT EXISTS idx_cl_container_recorded (container_id, recorded_at),
    ADD INDEX IF NOT EXISTS idx_cl_name_orphan_recorded (container_name, container_id, recorded_at);

ALTER TABLE lifecycle_logs
    ADD INDEX IF NOT EXISTS idx_ll_container_recorded (container_id, recorded_at),
    ADD INDEX IF NOT EXISTS idx_ll_name_orphan_recorded (container_name, container_id, recorded_at);
