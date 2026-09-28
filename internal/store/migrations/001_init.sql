CREATE TABLE chats (
  id               INTEGER PRIMARY KEY,
  type             TEXT    NOT NULL,
  title            TEXT    NOT NULL DEFAULT '',
  username         TEXT    NOT NULL DEFAULT '',
  folders_json     TEXT    NOT NULL DEFAULT '[]',
  migrated_from_id INTEGER,
  status           TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inaccessible')),
  first_seen_at    INTEGER NOT NULL,
  last_synced_at   INTEGER
);

CREATE TABLE topics (
  chat_id    INTEGER NOT NULL REFERENCES chats(id),
  topic_id   INTEGER NOT NULL,
  title      TEXT    NOT NULL,
  closed     INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (chat_id, topic_id)
);

CREATE TABLE users (
  id         INTEGER PRIMARY KEY,
  first_name TEXT    NOT NULL DEFAULT '',
  last_name  TEXT    NOT NULL DEFAULT '',
  username   TEXT    NOT NULL DEFAULT '',
  is_bot     INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

CREATE TABLE messages (
  chat_id             INTEGER NOT NULL REFERENCES chats(id),
  id                  INTEGER NOT NULL,
  topic_id            INTEGER,
  date                INTEGER NOT NULL,
  from_id             INTEGER,
  text                TEXT    NOT NULL DEFAULT '',
  entities_json       TEXT,
  reply_to_id         INTEGER,
  fwd_from_json       TEXT,
  grouped_id          INTEGER,
  reactions_json      TEXT,
  service_action_json TEXT,
  pinned              INTEGER NOT NULL DEFAULT 0,
  edit_date           INTEGER,
  raw                 BLOB    NOT NULL,
  tl_layer            INTEGER NOT NULL,
  PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE INDEX messages_by_topic ON messages (chat_id, topic_id, date);
CREATE INDEX messages_by_date  ON messages (chat_id, date);

CREATE TABLE media (
  key        TEXT    PRIMARY KEY,
  kind       TEXT    NOT NULL,
  file_name  TEXT    NOT NULL DEFAULT '',
  mime       TEXT    NOT NULL DEFAULT '',
  size       INTEGER NOT NULL DEFAULT 0,
  local_path TEXT,
  status     TEXT    NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed', 'skipped')),
  error      TEXT,
  attempts   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX media_by_status ON media (status);

CREATE TABLE message_media (
  chat_id    INTEGER NOT NULL,
  message_id INTEGER NOT NULL,
  media_key  TEXT    NOT NULL REFERENCES media(key),
  PRIMARY KEY (chat_id, message_id, media_key),
  FOREIGN KEY (chat_id, message_id) REFERENCES messages (chat_id, id)
);

CREATE INDEX message_media_by_key ON message_media (media_key);

CREATE TABLE sync_cursors (
  chat_id       INTEGER PRIMARY KEY REFERENCES chats(id),
  newest_id     INTEGER NOT NULL DEFAULT 0,
  oldest_id     INTEGER NOT NULL DEFAULT 0,
  backfill_done INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL
);
