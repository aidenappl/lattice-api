-- 014_destination_locality.sql
--
-- Where a backup destination physically lives, as an operator asserts it.
--
-- Lattice cannot infer this and must not guess. An S3 endpoint might be
-- OpenBucket running on the very worker being backed up, or a bucket in another
-- country; both are "type: s3" with a URL. A backup dashboard that guesses
-- "off-site ✓" wrongly is worse than no dashboard, because it converts an
-- unknown into false confidence.
--
-- So the default is `unknown` rather than anything reassuring, and posture
-- reports unknown as unknown until somebody confirms it.

ALTER TABLE backup_destinations
    ADD COLUMN IF NOT EXISTS locality VARCHAR(20) NOT NULL DEFAULT 'unknown';
