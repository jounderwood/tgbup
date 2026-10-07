-- A snapshot that could not read some files does not hold its update: the
-- next run snapshots the place again.
ALTER TABLE snapshots ADD COLUMN partial INTEGER NOT NULL DEFAULT 0;
