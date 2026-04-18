-- 008: Add container_name to container_logs for reliable log retrieval
ALTER TABLE container_logs ADD COLUMN container_name VARCHAR(255) DEFAULT NULL AFTER container_id;
CREATE INDEX idx_logs_container_name ON container_logs (container_name);
