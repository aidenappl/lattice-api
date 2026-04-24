-- Add performance indexes for frequently queried columns.
-- These are safe to run multiple times due to IF NOT EXISTS.

CREATE INDEX IF NOT EXISTS idx_containers_stack_active ON containers(stack_id, active);
CREATE INDEX IF NOT EXISTS idx_containers_status_active ON containers(status, active);
CREATE INDEX IF NOT EXISTS idx_deployments_status ON deployments(status);
CREATE INDEX IF NOT EXISTS idx_workers_status_active ON workers(status, active);
CREATE INDEX IF NOT EXISTS idx_audit_log_inserted_at ON audit_log(inserted_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_user_inserted ON audit_log(user_id, inserted_at);
