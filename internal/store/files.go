package store

import (
	"context"
	"database/sql"
)

// DownloadedFile is one appearance of a downloaded file in a message; a file
// forwarded to several chats appears once per message.
type DownloadedFile struct {
	LocalPath  string
	Kind       string
	FileName   string
	Date       int64
	ChatID     int64
	ChatTitle  string
	ChatType   string
	TopicID    int
	TopicTitle string
	// UpgradedTo is set for a basic group that was upgraded to a supergroup.
	UpgradedTo    int64
	UpgradedTitle string
}

func (s *Store) DownloadedFiles(ctx context.Context) ([]DownloadedFile, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.local_path, m.kind, m.file_name, msg.date, c.id, c.title, c.type,
       COALESCE(msg.topic_id, 0), COALESCE(t.title, ''), up.id, COALESCE(up.title, '')
FROM message_media mm
JOIN media m ON m.key = mm.media_key AND m.status = 'done'
JOIN messages msg ON msg.chat_id = mm.chat_id AND msg.id = mm.message_id
JOIN chats c ON c.id = mm.chat_id
LEFT JOIN topics t ON t.chat_id = msg.chat_id AND t.topic_id = msg.topic_id
LEFT JOIN chats up ON up.migrated_from_id = c.id
ORDER BY msg.date, mm.chat_id, mm.message_id, m.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadedFile
	for rows.Next() {
		var (
			f  DownloadedFile
			up sql.NullInt64
		)
		if err := rows.Scan(&f.LocalPath, &f.Kind, &f.FileName, &f.Date, &f.ChatID, &f.ChatTitle, &f.ChatType,
			&f.TopicID, &f.TopicTitle, &up, &f.UpgradedTitle); err != nil {
			return nil, err
		}
		f.UpgradedTo = up.Int64
		out = append(out, f)
	}
	return out, rows.Err()
}
