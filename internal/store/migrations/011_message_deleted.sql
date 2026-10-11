-- When tgbup found the message gone from Telegram (deleted there). The
-- archive keeps it; it is cleared if the message shows up again.
ALTER TABLE messages ADD COLUMN deleted_at INTEGER;
