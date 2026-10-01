-- History of scheduled runs, for the weekly summary and, later, a UI.
CREATE TABLE runs (
  id           INTEGER PRIMARY KEY,
  started_at   INTEGER NOT NULL,
  finished_at  INTEGER NOT NULL,
  ok           INTEGER NOT NULL,
  summary_json TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX runs_by_start ON runs (started_at);
