-- 017_automations.sql
--
-- Automations: named rules that pair ONE trigger with an ORDERED list of actions.
--
-- The motivating failure: a deploy token is bound to one stack, and its
-- ?container= parameter only narrows WITHIN that stack. monitor-core's CI posted
-- to one deploy URL with ?container=monitor-core, so its second zone was never
-- redeployed and ended up a release and eight migrations behind — under a green
-- CI history. An automation is one webhook that redeploys N containers wherever
-- they live, and records what happened to each of them.
--
-- ⚠️ THE SHAPE IS PERSISTED, NOT A SCRIPT. trigger_config and actions are JSON
-- documents carrying a `type` discriminator. A new action type is a new case in
-- the executor's one switch (automations/actions.go, kindFor) plus a new config
-- shape — never a new column. `trigger` is a reserved word, hence trigger_config.

CREATE TABLE IF NOT EXISTS automations (
    id                   INT AUTO_INCREMENT PRIMARY KEY,
    name                 VARCHAR(128) NOT NULL,
    description          TEXT         DEFAULT NULL,
    enabled              TINYINT(1)   NOT NULL DEFAULT 1,
    -- {"type":"webhook"} | {"type":"schedule","cron":"0 3 * * *"}  (cron is UTC)
    trigger_config       JSON         NOT NULL,
    -- [{"type":"redeploy_container","continue_on_error":false,"config":{...}}, ...]
    actions              JSON         NOT NULL,
    -- SHA-256 of the webhook secret; the plaintext is shown once, at creation or
    -- rotation, and never stored. A column rather than a JSON field because it is
    -- a lookup key and has to be indexed. NULL for schedule triggers and for
    -- deleted automations: MariaDB unique keys ignore NULLs, which is exactly the
    -- behaviour wanted here.
    webhook_token_hash   CHAR(64)     DEFAULT NULL,
    webhook_last_used_at DATETIME     DEFAULT NULL,
    -- The identity every action is authorised against at RUN time. It is set to
    -- whoever last defined what the automation does (create, edit, enable) rather
    -- than frozen at creation — otherwise editing someone else's automation would
    -- be a way to borrow their authority. See automations/authorize.go.
    run_as_user_id       INT          NOT NULL,
    created_by           INT          NOT NULL,
    -- Concurrency guard. Taken by a conditional UPDATE when a run starts and
    -- released when it ends. A claim older than the run budget is breakable, so a
    -- control plane that dies mid-run cannot wedge the automation forever.
    running_run_id       INT          DEFAULT NULL,
    running_since        DATETIME     DEFAULT NULL,
    active               TINYINT(1)   NOT NULL DEFAULT 1,
    inserted_at          TIMESTAMP    NOT NULL DEFAULT current_timestamp(),
    updated_at           TIMESTAMP    NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
    UNIQUE KEY idx_automations_webhook_token (webhook_token_hash),
    KEY idx_automations_active (active, enabled)
);

-- One row per firing, INCLUDING the firings that did not run: a disabled
-- automation, an overlap with a run still in progress, no free runner slot, a
-- schedule slot too old to catch up. An automation whose history you cannot see
-- is the same invisible failure the incident above was made of.
CREATE TABLE IF NOT EXISTS automation_runs (
    id             INT AUTO_INCREMENT PRIMARY KEY,
    automation_id  INT          NOT NULL,
    -- webhook | schedule | manual
    trigger_source VARCHAR(20)  NOT NULL,
    trigger_detail VARCHAR(255) DEFAULT NULL,
    -- Nominal, un-jittered UTC slot for schedule runs, NULL for everything else.
    -- The unique key below is the schedule's claim, exactly as in
    -- database_snapshot_runs: a second attempt at the same slot loses on insert,
    -- and NULLs (webhook and manual runs) never collide.
    scheduled_at   DATETIME     DEFAULT NULL,
    -- in_progress | succeeded | failed | skipped
    status         VARCHAR(20)  NOT NULL DEFAULT 'in_progress',
    skip_reason    VARCHAR(255) DEFAULT NULL,
    -- 1-based index of the FIRST step that failed.
    failed_step    INT          DEFAULT NULL,
    -- A failure that belongs to the run rather than to one step: authorisation,
    -- the run budget, or a control plane that died mid-run.
    error_message  TEXT         DEFAULT NULL,
    -- Per-step results, rewritten after every step so that a crash mid-run still
    -- leaves the progress it made visible.
    steps          JSON         DEFAULT NULL,
    run_as_user_id INT          DEFAULT NULL,
    triggered_by   INT          DEFAULT NULL,
    started_at     DATETIME     NOT NULL,
    finished_at    DATETIME     DEFAULT NULL,
    inserted_at    TIMESTAMP    NOT NULL DEFAULT current_timestamp(),
    updated_at     TIMESTAMP    NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
    UNIQUE KEY idx_automation_run_slot (automation_id, scheduled_at),
    KEY idx_automation_runs_automation (automation_id, id),
    KEY idx_automation_runs_status (status, started_at)
);

-- The audit actor for an automated action is the PAIR (user_id, automation_run_id).
-- user_id alone would record only whose authority was used, which reads exactly
-- like that person clicking the button. A non-NULL automation_run_id says the
-- call arrived via an automation, and which run of it.
ALTER TABLE audit_log
    ADD COLUMN IF NOT EXISTS automation_run_id INT DEFAULT NULL;

ALTER TABLE audit_log
    ADD INDEX IF NOT EXISTS idx_audit_log_automation_run (automation_run_id);
