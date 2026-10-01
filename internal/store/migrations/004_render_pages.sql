-- Fingerprint of each rendered month page, so a render redraws only what changed.
CREATE TABLE render_pages (
  chat_id     INTEGER NOT NULL,
  topic_id    INTEGER NOT NULL,
  month       TEXT    NOT NULL,
  fingerprint TEXT    NOT NULL,
  PRIMARY KEY (chat_id, topic_id, month)
);
