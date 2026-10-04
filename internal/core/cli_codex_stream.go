package core

// Codex streaming — drives `codex exec --json` and translates its JSONL
// event stream into StreamEvents. Codex has shipped two event schemas:
//
//   older (proto-style):  {"id":"0","msg":{"type":"agent_message_delta",
//                          "delta":"…"}} with exec_command_begin/end,
//                          patch_apply_begin/end, agent_message,
//                          task_complete, session_configured
//   newer (thread-style): {"type":"item.started","item":{"item_type":
//                          "command_execution", …}} / "item.completed",
//                          "thread.started", "turn.completed"
//
// The parser accepts both and ignores anything it doesn't recognise, so
// a codex upgrade degrades to fewer live events instead of breaking the
// turn. The final reply text still comes from --output-last-message
// (same file mechanism as the buffered path) with the streamed text as
// fallback.
//
// OpenCode-like adapters use their own JSON stream implementation.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func (a *execAdapter) SingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error) {
	if isOpenCodeLikeAdapter(a.name) {
		return a.openCodeSingleShotStream(ctx, opts, emit)
	}
	if a.name != "codex" {
		return nil, ErrStreamUnsupported
	}
	path, err := a.resolveBin()
	if err != nil {
		return nil, fmt.Errorf("%s CLI not on PATH", a.bin)
	}

	msg := opts.Message
	if s := strings.TrimSpace(opts.SystemPrompt); s != "" {
		msg = s + "\n\n" + msg
	}
	tmpDir, err := os.MkdirTemp("", "praimate-"+a.name+"-")
	if err != nil {
		return nil, fmt.Errorf("%s: scratch dir: %w", a.name, err)
	}
	defer os.RemoveAll(tmpDir)

	args, replyFile := a.build(buildIn{Message: msg, Model: opts.Model, ReasoningEffort: opts.ReasoningEffort, Tools: opts.Tools, TmpDir: tmpDir})
	// Insert --json right after the "exec" subcommand; the stdin "-"
	// sentinel must stay last, so we can't just append.
	args = append([]string{args[0], "--json"}, args[1:]...)

	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = mergeEnv(cmd.Environ(), opts.Env)
	cmd.Stdin = strings.NewReader(msg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex stream: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex stream: start: %w", err)
	}
	if emit != nil {
		emit(StreamEvent{Type: "status", Detail: "Codex process started; waiting for a session."})
	}

	streamed := parseCodexStream(stdout, emit)

	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if ctx.Err() == nil {
			return nil, fmt.Errorf("codex stream: %w (stderr=%s)", waitErr, truncate(stderr.String(), 400))
		}
	}

	// Final text: the --output-last-message file is authoritative (same
	// as the buffered path); streamed agent messages are the fallback.
	text := streamed.Text
	if replyFile != "" {
		if b, rerr := os.ReadFile(replyFile); rerr == nil && len(bytes.TrimSpace(b)) > 0 {
			text = string(b)
		}
	}
	text = strings.TrimRight(text, "\n")
	if ctx.Err() != nil {
		return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, ctx.Err()
	}
	if text == "" && exitCode != 0 {
		text = strings.TrimSpace(stderr.String())
	}
	if streamed.Err != nil || exitCode != 0 {
		return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, fmt.Errorf("codex stream: %w (stderr=%s)", firstErr(streamed.Err, fmt.Errorf("CLI exited with code %d", exitCode)), truncate(stderr.String(), 400))
	}
	return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, nil
}

func (a *execAdapter) ResumeStream(ctx context.Context, sessionID string, opts ResumeOpts, emit StreamHandler) (*Reply, error) {
	if isOpenCodeLikeAdapter(a.name) {
		return a.openCodeResumeStream(ctx, sessionID, opts, emit)
	}
	if a.name == "codex" {
		return a.codexResumeStream(ctx, sessionID, opts, emit)
	}
	return nil, ErrStreamUnsupported
}

func (a *execAdapter) codexResumeStream(ctx context.Context, sessionID string, opts ResumeOpts, emit StreamHandler) (*Reply, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("%s.ResumeStream: empty sessionID", a.name)
	}
	path, err := a.resolveBin()
	if err != nil {
		return nil, fmt.Errorf("%s CLI not on PATH", a.bin)
	}
	tmpDir, err := os.MkdirTemp("", "praimate-"+a.name+"-")
	if err != nil {
		return nil, fmt.Errorf("%s: scratch dir: %w", a.name, err)
	}
	defer os.RemoveAll(tmpDir)

	replyFile := filepath.Join(tmpDir, "reply.txt")
	args := []string{"exec", "resume", "--skip-git-repo-check", "--json", "--output-last-message", replyFile}
	if opts.Model != "" {
		args = append(args, "-m", opts.Model)
	}
	if opts.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(opts.ReasoningEffort))
	}
	args = append(args, codexPermissionArgs(opts.Tools, true)...)
	args = append(args, sessionID, "-")

	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = mergeEnv(cmd.Environ(), opts.Env)
	cmd.Stdin = strings.NewReader(opts.Message)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("codex resume stream: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex resume stream: start: %w", err)
	}
	if emit != nil {
		emit(StreamEvent{Type: "status", Detail: "Codex process started; resuming the session."})
	}

	streamed := parseCodexStream(stdout, emit)
	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if ctx.Err() == nil {
			return nil, fmt.Errorf("codex resume stream: %w (stderr=%s)", waitErr, truncate(stderr.String(), 400))
		}
	}

	text := streamed.Text
	if b, rerr := os.ReadFile(replyFile); rerr == nil && len(bytes.TrimSpace(b)) > 0 {
		text = string(b)
	}
	text = strings.TrimRight(text, "\n")
	if streamed.SessionID == "" {
		streamed.SessionID = sessionID
	}
	if ctx.Err() != nil {
		return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, ctx.Err()
	}
	if text == "" && exitCode != 0 {
		text = strings.TrimSpace(stderr.String())
	}
	if streamed.Err != nil || exitCode != 0 {
		return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, fmt.Errorf("codex resume stream: %w (stderr=%s)", firstErr(streamed.Err, fmt.Errorf("CLI exited with code %d", exitCode)), truncate(stderr.String(), 400))
	}
	return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, nil
}

// codexStreamLine tolerantly covers both codex event schemas. Item is
// kept as a raw map because its field names have shifted between
// releases.
type codexStreamLine struct {
	Type     string          `json:"type"`
	Message  string          `json:"message"`
	Error    json.RawMessage `json:"error"`
	Usage    map[string]any  `json:"usage"`
	ThreadID string          `json:"thread_id"`
	Item     map[string]any  `json:"item"`
	Msg      *struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		Delta     string          `json:"delta"`
		Message   string          `json:"message"`
		Command   json.RawMessage `json:"command"`
		ExitCode  *int            `json:"exit_code"`
		Error     json.RawMessage `json:"error"`
	} `json:"msg"`
}

type codexStreamResult struct {
	Text      string
	SessionID string
	Err       error
}

// parseCodexStream consumes the JSONL stream, emitting StreamEvents,
// and returns the accumulated assistant text (used as fallback when the
// --output-last-message file is missing).
func parseCodexStream(r io.Reader, emit StreamHandler) codexStreamResult {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	br := bufio.NewReaderSize(r, 256*1024)
	var acc strings.Builder
	var sessionID string
	var sawDelta bool
	var activeTurn bool
	var turnErr error
	for {
		raw, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var line codexStreamLine
			if jerr := json.Unmarshal(bytes.TrimSpace(raw), &line); jerr == nil {
				switch {
				case line.Type == "thread.started":
					sessionID = line.ThreadID
					emit(StreamEvent{Type: "status", Detail: "Codex session started.", ID: sessionID})
				case line.Type == "turn.started":
					activeTurn = true
					emit(StreamEvent{Type: "step_start", Detail: "turn"})
				case line.Type == "turn.completed":
					activeTurn = false
					if usage := reportedUsage(line.Usage, "input_tokens", "output_tokens"); usage != nil {
						emit(StreamEvent{Type: "usage", Usage: usage, OK: true})
					}
					emit(StreamEvent{Type: "step_finish", Detail: "turn"})
				case line.Type == "error" || line.Type == "turn.failed":
					detail := codexErrorMessage(line.Error, line.Message)
					if detail == "" {
						detail = "Codex reported " + line.Type
					}
					emit(StreamEvent{Type: "error", Detail: detail})
					// An error can describe a recoverable reconnect; only a failed
					// turn establishes failure independently of the exit code.
					if line.Type == "turn.failed" {
						turnErr = fmt.Errorf("codex turn failed: %s", detail)
					}
				case line.Msg != nil:
					if line.Msg.SessionID != "" {
						sessionID = line.Msg.SessionID
					}
					if line.Msg.Type == "task_started" {
						activeTurn = true
					} else if line.Msg.Type == "task_complete" {
						activeTurn = false
					}
					handleCodexProtoMsg(&line, &acc, &sawDelta, emit)
				case strings.HasPrefix(line.Type, "item."):
					handleCodexItem(line.Type, line.Item, &acc, sawDelta, emit)
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				turnErr = firstErr(turnErr, err)
			} else if activeTurn && turnErr == nil {
				turnErr = errors.New("codex stream ended before turn completion")
			}
			return codexStreamResult{Text: acc.String(), SessionID: sessionID, Err: turnErr}
		}
	}
}

// handleCodexProtoMsg covers the older {"msg":{...}} schema.
func handleCodexProtoMsg(line *codexStreamLine, acc *strings.Builder, sawDelta *bool, emit StreamHandler) {
	m := line.Msg
	switch m.Type {
	case "task_started":
		emit(StreamEvent{Type: "step_start", Detail: "task"})
	case "task_complete":
		emit(StreamEvent{Type: "step_finish", Detail: "task", OK: true})
	case "session_configured":
		emit(StreamEvent{Type: "status", Detail: "Codex session started.", ID: m.SessionID})
	case "agent_reasoning_delta":
		if m.Delta != "" {
			emit(StreamEvent{Type: "reasoning", Text: m.Delta})
		}
	case "error":
		emit(StreamEvent{Type: "error", Detail: codexErrorMessage(m.Error, m.Message)})
	case "agent_message_delta":
		if m.Delta != "" {
			*sawDelta = true
			acc.WriteString(m.Delta)
			emit(StreamEvent{Type: "text", Text: m.Delta})
		}
	case "agent_message":
		if !*sawDelta && m.Message != "" {
			acc.WriteString(m.Message)
			emit(StreamEvent{Type: "text", Text: m.Message})
		}
	case "exec_command_begin":
		emit(StreamEvent{Type: "tool_start", Tool: "shell", Detail: codexCommandString(m.Command)})
	case "exec_command_end":
		ok := m.ExitCode == nil || *m.ExitCode == 0
		emit(StreamEvent{Type: "tool_end", OK: ok})
	case "patch_apply_begin":
		emit(StreamEvent{Type: "tool_start", Tool: "apply_patch"})
	case "patch_apply_end":
		emit(StreamEvent{Type: "tool_end", OK: true})
	}
}

// handleCodexItem covers the newer {"type":"item.*","item":{...}}
// schema. Field names probed defensively: releases have used both
// "type" and "item_type" for the item kind.
func handleCodexItem(eventType string, item map[string]any, acc *strings.Builder, sawDelta bool, emit StreamHandler) {
	if item == nil {
		return
	}
	kind, _ := item["item_type"].(string)
	if kind == "" {
		kind, _ = item["type"].(string)
	}
	id, _ := item["id"].(string)
	switch kind {
	case "reasoning":
		if eventType == "item.started" {
			emit(StreamEvent{Type: "status", Detail: "Codex started reasoning.", ID: id})
		} else if eventType == "item.completed" {
			if text, _ := item["text"].(string); text != "" {
				emit(StreamEvent{Type: "reasoning", Text: text, ID: id})
			}
		}
	case "error":
		if eventType == "item.completed" {
			message, _ := item["message"].(string)
			emit(StreamEvent{Type: "error", Detail: message, ID: id})
		}
	case "mcp_tool_call", "web_search":
		tool, _ := item["tool"].(string)
		if tool == "" {
			tool = kind
		}
		detail, _ := item["server"].(string)
		if kind == "web_search" {
			detail, _ = item["query"].(string)
		}
		if eventType == "item.started" {
			emit(StreamEvent{Type: "tool_start", Tool: tool, Detail: truncate(detail, 160), ID: id})
		} else if eventType == "item.completed" {
			status, _ := item["status"].(string)
			emit(StreamEvent{Type: "tool_end", Tool: tool, ID: id, OK: status != "failed" && item["error"] == nil})
		}
	case "command_execution":
		detail, _ := item["command"].(string)
		if eventType == "item.started" {
			emit(StreamEvent{Type: "tool_start", Tool: "shell", Detail: truncate(detail, 160), ID: id})
		} else if eventType == "item.completed" {
			status, _ := item["status"].(string)
			exitCode, _ := item["exit_code"].(float64)
			emit(StreamEvent{Type: "tool_end", Tool: "shell", ID: id, OK: status != "failed" && exitCode == 0})
		}
	case "file_change", "patch":
		if eventType == "item.started" {
			emit(StreamEvent{Type: "tool_start", Tool: "apply_patch", ID: id})
		} else if eventType == "item.completed" {
			status, _ := item["status"].(string)
			emit(StreamEvent{Type: "tool_end", Tool: "apply_patch", ID: id, OK: status != "failed"})
		}
	case "agent_message":
		if eventType == "item.completed" && !sawDelta {
			if text, _ := item["text"].(string); text != "" {
				acc.WriteString(text)
				emit(StreamEvent{Type: "text", Text: text})
			}
		}
	}
}

func codexErrorMessage(raw json.RawMessage, fallback string) string {
	var message string
	if json.Unmarshal(raw, &message) != nil {
		var detail struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &detail) == nil {
			message = detail.Message
		}
	}
	if message == "" {
		message = fallback
	}
	return truncate(strings.TrimSpace(message), 1200)
}

// codexCommandString renders the exec_command_begin command field,
// which has been both a JSON array and a plain string across releases.
func codexCommandString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err == nil {
		return truncate(strings.Join(parts, " "), 160)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return truncate(s, 160)
	}
	return ""
}
