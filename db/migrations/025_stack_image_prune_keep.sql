-- 025_stack_image_prune_keep.sql
--
-- APL-86: after a successful deploy the runner removes the stack's unused old
-- images, keeping the newest image_prune_keep previous images per repository
-- so a rollback can start without a pull. -1 turns the prune off for the
-- stack. Before this, worker-6's disk filled to 100% in a day of deploys.
--
-- Additive with a default: existing stacks get 2, which runners without the
-- setting also assume.

ALTER TABLE stacks
    ADD COLUMN IF NOT EXISTS image_prune_keep INT NOT NULL DEFAULT 2 AFTER placement_constraints;
