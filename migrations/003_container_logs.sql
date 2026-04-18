-- 003: Add container_logs table for persisting worker/container log output

CREATE TABLE IF NOT EXISTS container_logs (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    container_id INT,
    worker_id    INT NOT NULL,
    stream       VARCHAR(10) NOT NULL DEFAULT 'stdout',
    message      TEXT NOT NULL,
    recorded_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_logs_container FOREIGN KEY (container_id) REFERENCES containers(id) ON DELETE SET NULL,
    CONSTRAINT fk_logs_worker FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
);

CREATE INDEX idx_logs_container ON container_logs (container_id);
CREATE INDEX idx_logs_worker ON container_logs (worker_id);
CREATE INDEX idx_logs_recorded ON container_logs (recorded_at);
