package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type skillTaskBudgetKey struct{}

// WithSkillTaskBudget identifies one host task across subruns/retries. This is
// orchestration input, not a model tool or a portable agent/package field.
func WithSkillTaskBudget(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, skillTaskBudgetKey{}, id)
}

func skillTaskBudgetID(ctx context.Context, fallback string) string {
	if id, ok := ctx.Value(skillTaskBudgetKey{}).(string); ok && id != "" {
		return id
	}
	return fallback
}

type skillTaskBudget struct {
	InputBytes int64 `json:"input_bytes"`
	SkillBytes int64 `json:"skill_bytes"`
	LimitBytes int64 `json:"limit_bytes"`
}

// Reserve only counters in the encrypted DB, before model transport. An atomic
// write transaction coordinates independent GUI/CLI processes. Reservations
// survive failed calls, new run IDs and retries; no prompt or event log is added.
func (c *Core) reserveSkillTaskInput(ctx context.Context, id string, input, skill, limit int64) error {
	if c.store == nil || id == "" || input < 0 || skill < 0 || skill > input || limit <= 0 {
		return errors.New("invalid controlled task budget reservation")
	}
	conn, err := c.store.DB().Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	key := "skill_task_budget:" + skillReview(id)
	var raw string
	err = conn.QueryRowContext(ctx, "SELECT value_json FROM settings_cli WHERE key = ?", key).Scan(&raw)
	ledger := skillTaskBudget{LimitBytes: limit}
	if err == nil {
		if err := json.Unmarshal([]byte(raw), &ledger); err != nil {
			return errors.New("invalid stored skill task budget")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if ledger.InputBytes < 0 || ledger.SkillBytes < 0 || ledger.SkillBytes > ledger.InputBytes || ledger.LimitBytes <= 0 {
		return errors.New("invalid stored skill task counters")
	}
	ledger.LimitBytes = min(ledger.LimitBytes, limit)
	if input > ledger.LimitBytes-ledger.InputBytes {
		return errors.New("cumulative_task_budget_exceeded: controlled input bytes exhausted across runs and attempts; no model call made")
	}
	ledger.InputBytes += input
	ledger.SkillBytes += skill
	body, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO settings_cli(key,value_json,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json,updated_at=excluded.updated_at`, key, string(body), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}
