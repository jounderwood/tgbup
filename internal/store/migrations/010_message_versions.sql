-- Telegram keeps only a message's latest version; the archive keeps the ones
-- before it, as tgbup found them. n = 1 is the oldest tgbup has.
CREATE TABLE message_versions (
  chat_id       INTEGER NOT NULL,
  id            INTEGER NOT NULL,
  n             INTEGER NOT NULL,
  text          TEXT    NOT NULL DEFAULT '',
  entities_json TEXT,
  media_keys    TEXT,             -- JSON array of the attachments it had
  edit_date     INTEGER,          -- Telegram's time of this version; NULL: as sent
  replaced_at   INTEGER NOT NULL, -- when tgbup found the next version
  PRIMARY KEY (chat_id, id, n)
) WITHOUT ROWID;
-- changed_at: when tgbup last found the message changed (an edit, reactions,
-- a poll's votes); checked_at: when tgbup last read it from Telegram.
ALTER TABLE messages ADD COLUMN changed_at INTEGER;
ALTER TABLE messages ADD COLUMN checked_at INTEGER;
