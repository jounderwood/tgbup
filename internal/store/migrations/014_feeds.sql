-- Where Telegram's change feeds stand: every run reads what changed since,
-- for edits and deletions of archived messages of any age. Box 0 is the
-- account's own feed (private chats and basic groups); any other box is the
-- chat id of a channel or supergroup, which has a feed of its own.
-- read_at: when a run last read it (0: never); the least recently read go
-- first.
CREATE TABLE feeds (
  box     INTEGER PRIMARY KEY,
  pts     INTEGER NOT NULL,
  qts     INTEGER NOT NULL DEFAULT 0,
  date    INTEGER NOT NULL DEFAULT 0,
  read_at INTEGER NOT NULL DEFAULT 0
);
