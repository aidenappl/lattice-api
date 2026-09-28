-- 018_deployment_status_validating.sql
--
-- The runner reports "validating" while it health-checks new containers, and
-- handleDeploymentProgress writes it to deployments.status. The column was
-- created by 001_initial.sql without that value, so every write failed with
-- Error 1265 (Data truncated for column 'status') and the deployment sat at
-- "deploying" until its terminal status landed.
--
-- Additive: the existing six values keep their order and meaning.

ALTER TABLE deployments
    MODIFY COLUMN status ENUM('pending', 'approved', 'deploying', 'deployed', 'failed', 'rolled_back', 'validating') NOT NULL DEFAULT 'pending';
