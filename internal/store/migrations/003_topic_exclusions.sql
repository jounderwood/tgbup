-- Topic exclusions last applied to a forum; a topic leaving this set means its
-- earlier messages were skipped and history has to be read again.
ALTER TABLE chats ADD COLUMN excluded_topics_json TEXT NOT NULL DEFAULT '[]';
