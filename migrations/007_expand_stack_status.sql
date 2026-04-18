-- Expand stack status enum to include 'deployed' and 'failed'
ALTER TABLE stacks MODIFY COLUMN status ENUM('active', 'stopped', 'deploying', 'deployed', 'failed', 'error') NOT NULL DEFAULT 'stopped';
