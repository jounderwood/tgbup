-- An update is a run (or a redraw) that changed the archive; n numbers them.
-- totals_json is what the archive held then, so the next update's diff is
-- what changed since, whatever changed it.
CREATE TABLE updates (
  n           INTEGER PRIMARY KEY,
  at          INTEGER NOT NULL,
  diff_json   TEXT    NOT NULL DEFAULT '{}',
  totals_json TEXT    NOT NULL DEFAULT '{}'
);
-- The snapshots tgbup made, and which update each holds; forgotten_at is
-- when retention removed it from its place.
CREATE TABLE snapshots (
  repo         TEXT    NOT NULL,
  id           TEXT    NOT NULL,
  update_n     INTEGER NOT NULL,
  at           INTEGER NOT NULL,
  forgotten_at INTEGER,
  PRIMARY KEY (repo, id)
);
CREATE INDEX snapshots_by_update ON snapshots (update_n);
