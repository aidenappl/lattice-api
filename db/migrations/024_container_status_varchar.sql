-- 024_container_status_varchar.sql
--
-- containers.status was created by 001_initial.sql as
-- ENUM('running', 'stopped', 'error', 'pending'), but two values reach it that
-- the enum never allowed:
--   - "restarting": handleContainerSync writes the runner's Docker-state mapping
--     verbatim, and lattice-runner reports a restarting container as such.
--   - "paused": handleContainerAction writes it on a pause action.
-- Each such write failed with Error 1265 (Data truncated for column 'status'),
-- so the container kept its previous status. On 2026-10-17 at 00:09 that was
-- proxy-api, stuck restarting during deployment #315, which Lattice went on
-- showing as running.
--
-- VARCHAR rather than a wider ENUM, as snapshot_replicas.status does (015):
-- MODIFY to an ENUM fails outright if any row holds a value missing from the
-- new list, and with the squashed baseline no longer in the repo there is no
-- certainty what every row holds. Converting to VARCHAR keeps every value
-- exactly, and is a no-op on a re-run. The values Lattice writes are listed in
-- structs.ContainerStatus.

ALTER TABLE containers
    MODIFY COLUMN status VARCHAR(32) NOT NULL DEFAULT 'pending';
