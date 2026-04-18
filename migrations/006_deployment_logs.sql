-- 006: Add deployment_logs table for verbose deployment progress tracking
CREATE TABLE IF NOT EXISTS deployment_logs (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    deployment_id INT NOT NULL,
    level         VARCHAR(20) NOT NULL DEFAULT 'info',
    stage         VARCHAR(100),
    message       TEXT NOT NULL,
    recorded_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_deployment_logs_deployment FOREIGN KEY (deployment_id) REFERENCES deployments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_deployment_logs_deployment ON deployment_logs (deployment_id);
CREATE INDEX IF NOT EXISTS idx_deployment_logs_recorded ON deployment_logs (recorded_at);
