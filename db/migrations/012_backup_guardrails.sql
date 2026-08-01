-- 012_backup_guardrails.sql — continues the sequence the original migrations left at 011
--
-- Guardrails around destroying a database, and somewhere to record what a data
-- volume actually costs.
--
-- Context: deleting a database now destroys its data volume (lattice-api
-- 25c3f5b, 2026-07-29). That made delete genuinely irreversible, which is
-- exactly when a deliberate guard and a last-chance backup stop being optional.
--
-- Every statement is written to be re-runnable: the runner retries a failed file
-- in full, and MariaDB's IF NOT EXISTS on ADD COLUMN is what makes that safe.

-- Refuse deletion outright while set. The equivalent of RDS deletion protection:
-- the point is not that it cannot be turned off, but that turning it off is a
-- separate, deliberate act from deleting.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS deletion_protection BOOLEAN NOT NULL DEFAULT 0;

-- Set when a delete asked for a final snapshot. The teardown is asynchronous, so
-- the intent has to outlive the request that expressed it: the volume may only
-- be destroyed once the snapshot this flag promises has completed.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS pending_final_snapshot BOOLEAN NOT NULL DEFAULT 0;

-- On-disk size of the data volume, as last observed by the worker.
--
-- Nothing in the platform has ever tracked what a database actually costs on
-- disk: the only disk figure collected anywhere is the worker's root filesystem
-- via statfs("/"). A database that fills its host takes every other container
-- with it, and today that is invisible until it happens.
ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS volume_size_bytes BIGINT DEFAULT NULL;

ALTER TABLE database_instances
    ADD COLUMN IF NOT EXISTS volume_size_checked_at DATETIME DEFAULT NULL;
