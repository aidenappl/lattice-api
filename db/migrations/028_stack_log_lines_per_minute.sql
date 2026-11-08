-- 028_stack_log_lines_per_minute.sql
--
-- APL-120: one chatty container wrote ~6 GB of log lines a day into
-- container_logs. Each container of a stack may now store at most
-- log_lines_per_minute lines per minute (the live log view still shows every
-- line); the rest are replaced by one "[lattice] N log lines dropped" row.
-- -1 turns the cap off for the stack.
--
-- Additive with a default: existing stacks get 1000 (structs.LOG_LINES_DEFAULT).

ALTER TABLE stacks
    ADD COLUMN IF NOT EXISTS log_lines_per_minute INT NOT NULL DEFAULT 1000 AFTER image_prune_keep;
