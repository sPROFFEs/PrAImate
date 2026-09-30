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
// Only codex implements streaming among the exec adapters; the others
// return ErrStreamUnsupported and the chat layer falls back to the
// buffered path.

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

	args, replyFile := a.build(buildIn{Message: msg, Model: opts.Model, Tools: opts.Tools, TmpDir: tmpDir})
	// Insert --json right after the "exec" subcommand; the stdin "-"
	// sentinel must stay last, so we can't just append.
	args = append([]string{args[0], "--json"}, args[1:]...)

	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = mergeEnv(os.Environ(), opts.Env)
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
	args = append(args, codexPermissionArgs(opts.Tools, true)...)
	args = append(args, sessionID, "-")

	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = mergeEnv(os.Environ(), opts.Env)
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
	if ctx.Err() != nil {
		return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, ctx.Err()
	}
	if text == "" && exitCode != 0 {
		text = strings.TrimSpace(stderr.String())
	}
	if streamed.SessionID == "" {
		streamed.SessionID = sessionID
	}
	return &Reply{Text: text, SessionID: streamed.SessionID, ExitCode: exitCode}, nil
}

// codexStreamLine tolerantly covers both codex event schemas. Item is
// kept as a raw map because its field names have shifted between
// releases.
type codexStreamLine struct {
	Type     string         `json:"type"`
	Usage    map[string]any `json:"usage"`
	ThreadID string         `json:"thread_id"`
	Item     map[string]any `json:"item"`
	Msg      *struct {
		Type      string          `json:"type"`
		SessionID string          `json:"session_id"`
		Delta     string          `json:"delta"`
		Message   string          `json:"message"`
		Command   json.RawMessage `json:"command"`
		ExitCode  *int            `json:"exit_code"`
	} `json:"msg"`
}

type codexStreamResult struct {
	Text      string
	SessionID string
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
	for {
		raw, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var line codexStreamLine
			if jerr := json.Unmarshal(bytes.TrimSpace(raw), &line); jerr == nil {
				switch {
				case line.Type == "thread.started":
					sessionID = line.ThreadID
				case line.Type == "turn.started":
					emit(StreamEvent{Type: "step_start", Detail: "turn"})
				case line.Type == "turn.completed":
					if usage := reportedUsage(line.Usage, "input_tokens", "output_tokens"); usage != nil {
						emit(StreamEvent{Type: "usage", Usage: usage, OK: true})
					}
					emit(StreamEvent{Type: "step_finish", Detail: "turn"})
				case line.Msg != nil:
					if line.Msg.SessionID != "" {
						sessionID = line.Msg.SessionID
					}
					handleCodexProtoMsg(&line, &acc, &sawDelta, emit)
				case strings.HasPrefix(line.Type, "item."):
					handleCodexItem(line.Type, line.Item, &acc, sawDelta, emit)
				}
			}
		}
		if err != nil {
			return codexStreamResult{Text: acc.String(), SessionID: sessionID}
		}
	}
}

// handleCodexProtoMsg covers the older {"msg":{...}} schema.
func handleCodexProtoMsg(line *codexStreamLine, acc *strings.Builder, sawDelta *bool, emit StreamHandler) {
	m := line.Msg
	switch m.Type {
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
	case "command_execution":
		detail, _ := item["command"].(string)
		if eventType == "item.started" {
			emit(StreamEvent{Type: "tool_start", Tool: "shell", Detail: truncate(detail, 160), ID: id})
		} else if eventType == "item.completed" {
			status, _ := item["status"].(string)
			emit(StreamEvent{Type: "tool_end", ID: id, OK: status != "failed"})
		}
	case "file_change", "patch":
		if eventType == "item.started" {
			emit(StreamEvent{Type: "tool_start", Tool: "apply_patch", ID: id})
		} else if eventType == "item.completed" {
			emit(StreamEvent{Type: "tool_end", ID: id, OK: true})
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
