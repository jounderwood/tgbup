-- A file's name is chosen before it is downloaded; path_key is that name as
-- APFS compares names (case and Unicode normalization ignored), so two files
-- never get names the disk would see as one.
ALTER TABLE media ADD COLUMN path_key TEXT;
CREATE UNIQUE INDEX media_by_path_key ON media (path_key);
