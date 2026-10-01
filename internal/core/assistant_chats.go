package core

import (
	"context"
	"errors"
)

// RecentChatMessages bounds Assistant context without reading entire histories.
func (c *Core) RecentChatMessages(ctx context.Context, id string, limit int) ([]Message, error) {
	if c.store == nil {
		return nil, errors.New("database unavailable")
	}
	if limit < 1 || limit > 20 {
		limit = 8
	}
	rows, err := c.store.DB().QueryContext(ctx, `SELECT id, chat_id, ts, role, content, tokens, meta_json FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		if len(m.Content) > 4096 {
			m.Content = m.Content[:4096] + " [truncated]"
		}
		out = append(out, *m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
