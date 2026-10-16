-- 022_worker_metrics_fleet_covering_index.sql
--
-- Covering index for the fleet-metrics history query (GetFleetMetricsHistory):
-- a recorded_at range with every column it aggregates, so the query never reads
-- table rows. The 7-day range reads ~550k samples; through idx_wm_recorded that
-- meant a row lookup each on a table larger than the buffer pool (~7.5 s in
-- production).
--
-- Building it on a large table takes a while and blocks API startup while it
-- runs. On an existing install, build it online first with the same statement
-- (InnoDB builds secondary indexes without blocking writes); this then no-ops.

CREATE INDEX IF NOT EXISTS idx_wm_fleet_cover ON worker_metrics (
    recorded_at,
    worker_id,
    cpu_percent,
    memory_used_mb,
    memory_total_mb,
    network_rx_rate,
    network_tx_rate,
    container_count,
    container_running_count
);
