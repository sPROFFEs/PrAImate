-- Stored in the existing AES-XTS encrypted database, including indexes.
-- No prompts, responses, workspace paths or credentials are recorded here.
CREATE TABLE usage_runs (
  id TEXT PRIMARY KEY,
  started_at TEXT NOT NULL,
  cli TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  surface TEXT NOT NULL,
  outcome TEXT NOT NULL DEFAULT 'running',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  reported_calls INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_usage_runs_started_at ON usage_runs(started_at);
