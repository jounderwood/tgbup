-- A version keeps the meta of before (a checklist's items, a link preview).
ALTER TABLE message_versions ADD COLUMN meta_json TEXT;
-- read_upto_id: for a checklist, the chat's newest message id when tgbup last
-- read it from Telegram; its state has every tick with an id up to there.
-- (checked_at, by the clock, is no longer used.)
ALTER TABLE messages ADD COLUMN read_upto_id INTEGER;
CREATE INDEX messages_changed ON messages (changed_at) WHERE changed_at IS NOT NULL;
CREATE INDEX messages_deleted ON messages (deleted_at) WHERE deleted_at IS NOT NULL;
