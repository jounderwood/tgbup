-- Names of chats/channels seen only as forward sources or senders; not archived chats.
CREATE TABLE peers (
  id         INTEGER PRIMARY KEY,
  title      TEXT    NOT NULL DEFAULT '',
  username   TEXT    NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
