package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// SaveWorkerGraphSnapshot keeps one current graph snapshot instead of appending
// its diffs at every state transition. Full task/activity history lives inside
// the snapshot. Hierarchical chats retain their existing message history.
func (c *Core) SaveWorkerGraphSnapshot(ctx context.Context, id, body string) error {
	var snapshot struct {
		ID  string          `json:"id"`
		DAG json.RawMessage `json:"dag"`
	}
	if len(body) > 64<<20 || json.Unmarshal([]byte(body), &snapshot) != nil || snapshot.ID != id || len(snapshot.DAG) == 0 || string(snapshot.DAG) == "null" {
		return errors.New("invalid worker graph snapshot")
	}
	chat, err := c.GetChat(ctx, id)
	if err != nil {
		return err
	}
	if chat.Settings.Surface != "workers" {
		return errors.New("graph snapshots require a worker chat")
	}
	tx, err := c.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE messages SET content=?,ts=? WHERE id=(SELECT id FROM messages WHERE chat_id=? AND role='assistant' ORDER BY id DESC LIMIT 1)`, body, now, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO messages (chat_id,ts,role,content,meta_json) VALUES (?,?,'assistant',?,'{}')`, id, now, body); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE chats SET updated_at=? WHERE id=?`, now, id); err != nil {
		return err
	}
	return tx.Commit()
}
