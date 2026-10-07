package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SaveWorkerGraphSnapshot keeps one current graph snapshot instead of appending
// its diffs at every state transition. Attempt summaries stay in the snapshot;
// new activity is retained separately in the encrypted message journal.
func (c *Core) SaveWorkerGraphSnapshot(ctx context.Context, id, body string) error {
	var snapshot struct {
		ID  string          `json:"id"`
		DAG json.RawMessage `json:"dag"`
	}
	if len(body) > 64<<20 || json.Unmarshal([]byte(body), &snapshot) != nil || snapshot.ID != id || len(snapshot.DAG) == 0 || string(snapshot.DAG) == "null" {
		return errors.New("invalid worker graph snapshot")
	}
	return c.SaveWorkerRunSnapshot(ctx, id, body, nil)
}

type workerActivityMeta struct {
	Type     string `json:"type"`
	WorkerID string `json:"workerID"`
}

// SaveWorkerRunSnapshot commits the summary and its activity journal together
// in the existing encrypted message store. No second database is introduced.
func (c *Core) SaveWorkerRunSnapshot(ctx context.Context, id, body string, activity []json.RawMessage) error {
	var snapshot struct {
		ID string `json:"id"`
	}
	if len(body) > 64<<20 || json.Unmarshal([]byte(body), &snapshot) != nil || snapshot.ID != id {
		return errors.New("invalid worker snapshot")
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
	for _, item := range activity {
		var event struct {
			WorkerID string `json:"workerID"`
		}
		// A 64 KiB event text can grow sixfold when JSON escapes its bytes.
		if len(item) > 512<<10 || json.Unmarshal(item, &event) != nil {
			return errors.New("invalid worker activity")
		}
		meta, _ := json.Marshal(workerActivityMeta{Type: "worker_activity", WorkerID: event.WorkerID})
		if _, err = tx.ExecContext(ctx, `INSERT INTO messages (chat_id,ts,role,content,meta_json) VALUES (?,?,'system',?,?)`, id, now, string(item), string(meta)); err != nil {
			return err
		}
	}
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

type WorkerActivityPage struct {
	Items   []json.RawMessage `json:"items"`
	Before  int64             `json:"before"`
	HasMore bool              `json:"hasMore"`
}

func (c *Core) WorkerActivity(ctx context.Context, id, workerID string, before int64, limit int) (WorkerActivityPage, error) {
	page := WorkerActivityPage{Items: []json.RawMessage{}}
	chat, err := c.GetChat(ctx, id)
	if err != nil {
		return page, err
	}
	if chat.Settings.Surface != "workers" || before < 0 {
		return page, errors.New("invalid worker activity request")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT id,content FROM messages WHERE chat_id=? AND role='system' AND meta_json LIKE '{"type":"worker_activity",%'`
	args := []any{id}
	if workerID != "" {
		meta, _ := json.Marshal(workerActivityMeta{Type: "worker_activity", WorkerID: workerID})
		query += ` AND meta_json=?`
		args = append(args, string(meta))
	}
	if before > 0 {
		query += ` AND id<?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := c.store.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var rowID int64
		var content string
		if err = rows.Scan(&rowID, &content); err != nil {
			return page, err
		}
		if len(page.Items) == limit {
			page.HasMore = true
			break
		}
		page.Before = rowID
		page.Items = append(page.Items, json.RawMessage(content))
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page, nil
}

// LoadWorkerRun reads only the creation config and latest snapshot; replaying
// the whole journal on startup would make long-running projects expensive.
func (c *Core) LoadWorkerRun(ctx context.Context, id string) (json.RawMessage, json.RawMessage, error) {
	var config, snapshot string
	if err := c.store.DB().QueryRowContext(ctx, `SELECT content FROM messages WHERE chat_id=? AND role='system' ORDER BY id LIMIT 1`, id).Scan(&config); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	err := c.store.DB().QueryRowContext(ctx, `SELECT content FROM messages WHERE chat_id=? AND role='assistant' ORDER BY id DESC LIMIT 1`, id).Scan(&snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return json.RawMessage(config), json.RawMessage(snapshot), err
}
