-- Non-file media (link preview, location, contact, poll, checklist, …) as one
-- extensible JSON object keyed by kind.
ALTER TABLE messages ADD COLUMN meta_json TEXT;
