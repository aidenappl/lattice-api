-- 019_container_ulimits.sql
--
-- Per-container resource limits from compose `ulimits` (e.g. ClickHouse's
-- nofile 262144). Stored as JSON, normalized to {"name": {"soft": n, "hard": n}},
-- like health_check. Before this column, compose ulimits were parsed by nothing
-- and silently dropped, so containers ran on the daemon's defaults.
--
-- Additive and nullable: existing rows mean "no ulimits", which is today's behaviour.

ALTER TABLE containers
    ADD COLUMN IF NOT EXISTS ulimits TEXT NULL AFTER health_check;
