-- 026_container_logs_name_recorded_index.sql
--
-- APL-113: ListContainerLogs matched `container_id = ? OR (container_id IS NULL
-- AND container_name = ?)` ordered by recorded_at. The OR left MariaDB no
-- per-container index, so it walked idx_cl_recorded backwards over every
-- container's logs (>10s for a quiet container). The query is now a UNION ALL
-- of two branches: the container_id branch uses uq_container_log_dedup
-- (container_id, recorded_at, ...), and this index serves the container_name
-- branch, and the name-only filter, in recorded_at order.
--
-- Adding a secondary index is an online operation in InnoDB (no table lock).

ALTER TABLE container_logs
    ADD INDEX IF NOT EXISTS idx_cl_name_recorded (container_name, recorded_at);
