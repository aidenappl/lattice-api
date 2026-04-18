-- 002: Expand worker_metrics with new columns from runner v2
-- Safe to run multiple times — each ALTER is guarded with IF NOT EXISTS

ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS cpu_cores INT AFTER cpu_percent;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS load_avg_1 FLOAT AFTER cpu_cores;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS load_avg_5 FLOAT AFTER load_avg_1;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS load_avg_15 FLOAT AFTER load_avg_5;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS memory_free_mb FLOAT AFTER memory_total_mb;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS swap_used_mb FLOAT AFTER memory_free_mb;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS swap_total_mb FLOAT AFTER swap_used_mb;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS container_running_count INT AFTER container_count;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS uptime_seconds FLOAT AFTER network_tx_bytes;
ALTER TABLE worker_metrics ADD COLUMN IF NOT EXISTS process_count INT AFTER uptime_seconds;

-- 002: Remove keyring_secret_key from registries (Lattice is self-contained)
ALTER TABLE registries DROP COLUMN IF EXISTS keyring_secret_key;
ALTER TABLE registries DROP COLUMN IF EXISTS auth_config;
ALTER TABLE registries ADD COLUMN IF NOT EXISTS username VARCHAR(255) AFTER type;
ALTER TABLE registries ADD COLUMN IF NOT EXISTS password VARCHAR(512) AFTER username;
