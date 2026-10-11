-- The audit reads a chat's whole history again, a few pages a run, to find
-- edits, reactions and deletions older than the window each run rereads.
-- audit_before_id: where the current round goes on (0: from the newest);
-- audited_at: when the last full round ended.
ALTER TABLE sync_cursors ADD COLUMN audit_before_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_cursors ADD COLUMN audited_at INTEGER;
