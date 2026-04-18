-- 009: Add healthcheck config and live health status to containers

ALTER TABLE containers
    ADD COLUMN health_check TEXT DEFAULT NULL AFTER entrypoint,
    ADD COLUMN health_status ENUM('healthy', 'unhealthy', 'starting', 'none') NOT NULL DEFAULT 'none' AFTER health_check;
