package core

// Streaming layer — what makes the GUI chat feel like the Codex /
// Claude desktop apps instead of "send and wait". CLIs that expose an
// event stream in headless mode (claude/openclaude: --output-format
// stream-json; codex: exec --json; opencode: run --format json)
// implement streamingAdapter; the
// chat layer forwards StreamEvents to the UI as they arrive: text
// deltas render token-by-token, tool calls show as a live activity
// feed, and the in-flight process can be interrupted via context
// cancellation.
//
// Adapters without an event stream (gemini, deepseek) simply
// don't implement the interface (or return ErrStreamUnsupported) and
// the chat falls back to the buffered single-shot path — same behavior
// as before, just without live updates.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// StreamEvent is one live update emitted while a turn is running.
type StreamEvent struct {
	// Type is one of:
	//   "text"        — Text holds an assistant output delta (append it)
	//   "reasoning"   — Text holds a visible thinking/reasoning update
	//   "step_start"  — one OpenCode agent step started
	//   "step_finish" — one OpenCode agent step finished
	//   "tool_start"  — the agent began a tool call (Tool + Detail set)
	//   "tool_end"    — a tool call finished (ID matches its start; OK
	//                   reports success)
	//   "error"       — Detail holds a recoverable runtime error
	Type string `json:"type"`
	// Text is the output delta for "text" events.
	Text string `json:"text,omitempty"`
	// Tool is the tool / command name for tool events ("Bash", "Edit",
	// "shell", "apply_patch", …).
	Tool string `json:"tool,omitempty"`
	// Detail is a one-line summary of the tool input (the command, the
	// file path, …), pre-truncated for display.
	Detail string `json:"detail,omitempty"`
	// ID correlates tool_end with its tool_start when the CLI provides
	// call ids; empty otherwise (match the oldest unfinished call).
	ID string `json:"id,omitempty"`
	// OK is meaningful on tool_end: false when the tool errored.
	OK bool `json:"ok,omitempty"`
	// Raw carries the original backend event/part when preserving it is
	// useful for later UI upgrades. It is intentionally opaque.
	Raw map[string]any `json:"raw,omitempty"`
	// Usage is normalized once by each adapter, including zero-valued reports.
	Usage *NativeUsage `json:"usage,omitempty"`
	Model string       `json:"model,omitempty"`
}

// StreamHandler receives events as the turn runs. Called from the
// adapter's reader goroutine — keep it fast and non-blocking.
type StreamHandler func(StreamEvent)

// ErrStreamUnsupported tells the caller to fall back to the buffered
// SingleShot/Resume path. Returned before any work happens.
var ErrStreamUnsupported = errors.New("adapter does not support streaming")

// streamingAdapter is the optional capability interface next to
// CLIAdapter. The chat layer type-asserts for it when the caller wants
// live events.
type streamingAdapter interface {
	SingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error)
	ResumeStream(ctx context.Context, sessionID string, opts ResumeOpts, emit StreamHandler) (*Reply, error)
}

// summarizeToolInput renders a tool's input as a one-line detail for
// the activity feed: prefer the obviously-human field (command, file
// path, pattern), fall back to compact JSON.
func summarizeToolInput(input map[string]any) string {
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "query", "description"} {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return truncate(strings.ReplaceAll(s, "\n", " ⏎ "), 160)
			}
		}
	}
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		raw, _ := json.Marshal(input[k])
		parts = append(parts, fmt.Sprintf("%s=%s", k, raw))
	}
	return truncate(strings.Join(parts, " "), 160)
}
