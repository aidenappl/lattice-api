-- Lattice Orchestrator Schema
-- MariaDB

CREATE TABLE IF NOT EXISTS users (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    email         VARCHAR(255) NOT NULL UNIQUE,
    name          VARCHAR(255),
    auth_type     ENUM('oauth', 'local') NOT NULL DEFAULT 'local',
    password_hash VARCHAR(255),
    forta_id      BIGINT UNIQUE,
    role          ENUM('admin', 'viewer') NOT NULL DEFAULT 'viewer',
    active        TINYINT(1) NOT NULL DEFAULT 1,
    updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP()
);

CREATE INDEX idx_users_email ON users (email);
CREATE INDEX idx_users_forta_id ON users (forta_id);
CREATE INDEX idx_users_active ON users (active);

-- ---

CREATE TABLE IF NOT EXISTS workers (
    id                INT AUTO_INCREMENT PRIMARY KEY,
    name              VARCHAR(255) NOT NULL,
    hostname          VARCHAR(255) NOT NULL,
    ip_address        VARCHAR(45),
    status            ENUM('online', 'offline', 'maintenance') NOT NULL DEFAULT 'offline',
    os                VARCHAR(100),
    arch              VARCHAR(50),
    docker_version    VARCHAR(50),
    last_heartbeat_at TIMESTAMP NULL,
    labels            TEXT,
    active            TINYINT(1) NOT NULL DEFAULT 1,
    updated_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP()
);

CREATE INDEX idx_workers_status ON workers (status);
CREATE INDEX idx_workers_active ON workers (active);

-- ---

CREATE TABLE IF NOT EXISTS worker_tokens (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    worker_id    INT NOT NULL,
    name         VARCHAR(255) NOT NULL,
    token_hash   VARCHAR(64) NOT NULL,
    last_used_at TIMESTAMP NULL,
    active       TINYINT(1) NOT NULL DEFAULT 1,
    updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_worker_tokens_worker FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
);

CREATE INDEX idx_worker_tokens_hash ON worker_tokens (token_hash);
CREATE INDEX idx_worker_tokens_worker ON worker_tokens (worker_id);
CREATE INDEX idx_worker_tokens_active ON worker_tokens (active);

-- ---

CREATE TABLE IF NOT EXISTS registries (
    id               INT AUTO_INCREMENT PRIMARY KEY,
    name             VARCHAR(255) NOT NULL,
    url              VARCHAR(512) NOT NULL,
    type             ENUM('dockerhub', 'ghcr', 'custom') NOT NULL DEFAULT 'custom',
    username         VARCHAR(255),
    password         VARCHAR(512),
    active           TINYINT(1) NOT NULL DEFAULT 1,
    updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP()
);

-- ---

CREATE TABLE IF NOT EXISTS stacks (
    id                  INT AUTO_INCREMENT PRIMARY KEY,
    name                VARCHAR(255) NOT NULL,
    description         TEXT,
    worker_id           INT,
    status              ENUM('active', 'stopped', 'deploying', 'error') NOT NULL DEFAULT 'stopped',
    deployment_strategy ENUM('rolling', 'blue-green', 'canary') NOT NULL DEFAULT 'rolling',
    auto_deploy         TINYINT(1) NOT NULL DEFAULT 0,
    active              TINYINT(1) NOT NULL DEFAULT 1,
    updated_at          TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_stacks_worker FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE SET NULL
);

CREATE INDEX idx_stacks_worker ON stacks (worker_id);
CREATE INDEX idx_stacks_status ON stacks (status);
CREATE INDEX idx_stacks_active ON stacks (active);

-- ---

CREATE TABLE IF NOT EXISTS containers (
    id              INT AUTO_INCREMENT PRIMARY KEY,
    stack_id        INT NOT NULL,
    name            VARCHAR(255) NOT NULL,
    image           VARCHAR(512) NOT NULL,
    tag             VARCHAR(255) NOT NULL DEFAULT 'latest',
    status          ENUM('running', 'stopped', 'error', 'pending') NOT NULL DEFAULT 'pending',
    port_mappings   TEXT,
    env_vars        TEXT,
    volumes         TEXT,
    cpu_limit       FLOAT,
    memory_limit    INT,
    replicas        INT NOT NULL DEFAULT 1,
    restart_policy  VARCHAR(50) DEFAULT 'unless-stopped',
    command         TEXT,
    entrypoint      TEXT,
    registry_id     INT,
    active          TINYINT(1) NOT NULL DEFAULT 1,
    updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_containers_stack FOREIGN KEY (stack_id) REFERENCES stacks(id) ON DELETE CASCADE,
    CONSTRAINT fk_containers_registry FOREIGN KEY (registry_id) REFERENCES registries(id) ON DELETE SET NULL
);

CREATE INDEX idx_containers_stack ON containers (stack_id);
CREATE INDEX idx_containers_status ON containers (status);
CREATE INDEX idx_containers_active ON containers (active);

-- ---

CREATE TABLE IF NOT EXISTS networks (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    stack_id    INT NOT NULL,
    name        VARCHAR(255) NOT NULL,
    driver      VARCHAR(50) NOT NULL DEFAULT 'bridge',
    subnet      VARCHAR(50),
    options     TEXT,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_networks_stack FOREIGN KEY (stack_id) REFERENCES stacks(id) ON DELETE CASCADE
);

CREATE INDEX idx_networks_stack ON networks (stack_id);

-- ---

CREATE TABLE IF NOT EXISTS volumes (
    id          INT AUTO_INCREMENT PRIMARY KEY,
    stack_id    INT NOT NULL,
    name        VARCHAR(255) NOT NULL,
    driver      VARCHAR(50) NOT NULL DEFAULT 'local',
    mount_path  VARCHAR(512),
    options     TEXT,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_volumes_stack FOREIGN KEY (stack_id) REFERENCES stacks(id) ON DELETE CASCADE
);

CREATE INDEX idx_volumes_stack ON volumes (stack_id);

-- ---

CREATE TABLE IF NOT EXISTS deployments (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    stack_id      INT NOT NULL,
    status        ENUM('pending', 'approved', 'deploying', 'deployed', 'failed', 'rolled_back') NOT NULL DEFAULT 'pending',
    strategy      ENUM('rolling', 'blue-green', 'canary') NOT NULL DEFAULT 'rolling',
    triggered_by  INT,
    approved_by   INT,
    started_at    TIMESTAMP NULL,
    completed_at  TIMESTAMP NULL,
    updated_at    TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_deployments_stack FOREIGN KEY (stack_id) REFERENCES stacks(id) ON DELETE CASCADE,
    CONSTRAINT fk_deployments_triggered_by FOREIGN KEY (triggered_by) REFERENCES users(id) ON DELETE SET NULL,
    CONSTRAINT fk_deployments_approved_by FOREIGN KEY (approved_by) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_deployments_stack ON deployments (stack_id);
CREATE INDEX idx_deployments_status ON deployments (status);

-- ---

CREATE TABLE IF NOT EXISTS deployment_containers (
    id              INT AUTO_INCREMENT PRIMARY KEY,
    deployment_id   INT NOT NULL,
    container_id    INT NOT NULL,
    image           VARCHAR(512) NOT NULL,
    tag             VARCHAR(255) NOT NULL,
    previous_image  VARCHAR(512),
    previous_tag    VARCHAR(255),
    status          ENUM('pending', 'pulling', 'starting', 'running', 'failed', 'rolled_back') NOT NULL DEFAULT 'pending',
    updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP() ON UPDATE CURRENT_TIMESTAMP(),
    inserted_at     TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_dc_deployment FOREIGN KEY (deployment_id) REFERENCES deployments(id) ON DELETE CASCADE,
    CONSTRAINT fk_dc_container FOREIGN KEY (container_id) REFERENCES containers(id) ON DELETE CASCADE
);

CREATE INDEX idx_dc_deployment ON deployment_containers (deployment_id);
CREATE INDEX idx_dc_container ON deployment_containers (container_id);

-- ---

CREATE TABLE IF NOT EXISTS worker_metrics (
    id                      INT AUTO_INCREMENT PRIMARY KEY,
    worker_id               INT NOT NULL,
    cpu_percent             FLOAT,
    cpu_cores               INT,
    load_avg_1              FLOAT,
    load_avg_5              FLOAT,
    load_avg_15             FLOAT,
    memory_used_mb          FLOAT,
    memory_total_mb         FLOAT,
    memory_free_mb          FLOAT,
    swap_used_mb            FLOAT,
    swap_total_mb           FLOAT,
    disk_used_mb            FLOAT,
    disk_total_mb           FLOAT,
    container_count         INT,
    container_running_count INT,
    network_rx_bytes        BIGINT,
    network_tx_bytes        BIGINT,
    uptime_seconds          FLOAT,
    process_count           INT,
    recorded_at             TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_metrics_worker FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
);

CREATE INDEX idx_metrics_worker ON worker_metrics (worker_id);
CREATE INDEX idx_metrics_recorded ON worker_metrics (recorded_at);

-- ---

CREATE TABLE IF NOT EXISTS container_events (
    id           INT AUTO_INCREMENT PRIMARY KEY,
    container_id INT,
    worker_id    INT NOT NULL,
    event_type   VARCHAR(100) NOT NULL,
    message      TEXT,
    recorded_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_events_container FOREIGN KEY (container_id) REFERENCES containers(id) ON DELETE SET NULL,
    CONSTRAINT fk_events_worker FOREIGN KEY (worker_id) REFERENCES workers(id) ON DELETE CASCADE
);

CREATE INDEX idx_events_container ON container_events (container_id);
CREATE INDEX idx_events_worker ON container_events (worker_id);
CREATE INDEX idx_events_recorded ON container_events (recorded_at);

-- ---

CREATE TABLE IF NOT EXISTS audit_log (
    id            INT AUTO_INCREMENT PRIMARY KEY,
    user_id       INT,
    action        VARCHAR(100) NOT NULL,
    resource_type VARCHAR(100) NOT NULL,
    resource_id   INT,
    details       TEXT,
    ip_address    VARCHAR(45),
    inserted_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP(),
    CONSTRAINT fk_audit_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_audit_user ON audit_log (user_id);
CREATE INDEX idx_audit_action ON audit_log (action);
CREATE INDEX idx_audit_resource ON audit_log (resource_type, resource_id);
CREATE INDEX idx_audit_inserted ON audit_log (inserted_at);
