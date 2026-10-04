package core

// OpenCode streaming/resume support for `opencode run --format json`.
// This keeps PrAImate's phase-1 integration CLI-based while preserving
// OpenCode's native session id and the structured reasoning/tool timeline
// that OpenCode emits.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

func isOpenCodeLikeAdapter(name string) bool {
	return name == "opencode" || name == "praimate-code"
}

func (a *execAdapter) openCodeSingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error) {
	msg := opts.Message
	if s := strings.TrimSpace(opts.SystemPrompt); s != "" {
		msg = s + "\n\n" + msg
	}
	return a.runOpenCodeJSON(ctx, msg, opts.Cwd, "", opts.Model, opts.Tools, opts.Env, emit)
}

func (a *execAdapter) openCodeResumeStream(ctx context.Context, sessionID string, opts ResumeOpts, emit StreamHandler) (*Reply, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("%s.ResumeStream: empty sessionID", a.name)
	}
	return a.runOpenCodeJSON(ctx, opts.Message, opts.Cwd, sessionID, opts.Model, opts.Tools, opts.Env, emit)
}

func (a *execAdapter) runOpenCodeJSON(ctx context.Context, message, cwd, sessionID, model, tools string, env map[string]string, emit StreamHandler) (*Reply, error) {
	path, err := a.resolveBin()
	if err != nil {
		return nil, fmt.Errorf("%s CLI not on PATH", a.bin)
	}
	args := []string{"run", "--format", "json", "--thinking"}
	args = append(args, openCodeModeArgs(tools)...)
	if sessionID != "" {
		args = append(args, "--session", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	cmd.Stdin = strings.NewReader(message)
	if cwd != "" {
		cmd.Dir = cwd
	}
	// Environ updates PWD after Dir is set. OpenCode resolves its workspace
	// from PWD, so inheriting the GUI's launch directory selects a different
	// project/configuration even though the process cwd is correct.
	cmd.Env = mergeEnv(cmd.Environ(), env)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s stream: stdout pipe: %w", a.name, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s stream: start: %w", a.name, err)
	}
	if emit != nil {
		emit(StreamEvent{Type: "status", Detail: a.name + " process started; waiting for a session."})
	}

	reply, parseErr := parseOpenCodeStream(stdout, emit)

	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if ctx.Err() == nil {
			return reply, fmt.Errorf("%s stream: %w (stderr=%s)", a.name, waitErr, truncate(stderr.String(), 400))
		}
	}
	if reply == nil {
		reply = &Reply{}
	}
	reply.ExitCode = exitCode
	if ctx.Err() != nil {
		return reply, ctx.Err()
	}
	if reply.Text == "" && exitCode != 0 {
		reply.Text = strings.TrimSpace(stderr.String())
	}
	if parseErr != nil || exitCode != 0 {
		msg := fmt.Sprintf("%s stream: %v", a.name, firstErr(parseErr, fmt.Errorf("CLI exited with code %d", exitCode)))
		if stderrText := strings.TrimSpace(stderr.String()); stderrText != "" {
			msg += fmt.Sprintf(" (stderr=%s)", truncate(stderrText, 400))
		}
		return reply, errors.New(msg)
	}
	return reply, nil
}

func openCodeModeArgs(tools string) []string {
	switch tools {
	case "plan":
		return []string{"--agent", "plan"}
	case "full":
		return []string{"--dangerously-skip-permissions"}
	}
	return nil
}

type openCodeStreamLine struct {
	Type      string         `json:"type"`
	SessionID string         `json:"sessionID"`
	Timestamp int64          `json:"timestamp"`
	Part      map[string]any `json:"part"`
	Error     any            `json:"error"`
	Field     string         `json:"field"`
	Delta     string         `json:"delta"`
	Payload   map[string]any `json:"payload"`
}

func parseOpenCodeStream(r io.Reader, emit StreamHandler) (*Reply, error) {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	br := bufio.NewReaderSize(r, 256*1024)
	var (
		acc        strings.Builder
		plain      strings.Builder
		sessionID  string
		firstErr   error
		activeStep bool
	)
	for {
		raw, err := br.ReadBytes('\n')
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 {
			var rawMap map[string]any
			if jerr := json.Unmarshal(trimmed, &rawMap); jerr == nil {
				line := decodeOpenCodeLine(rawMap)
				if line.Type == "step_start" || line.Type == "message.part.updated" && stringFromMap(line.Part, "type") == "step-start" {
					activeStep = true
				} else if line.Type == "step_finish" || line.Type == "message.part.updated" && stringFromMap(line.Part, "type") == "step-finish" {
					activeStep = false
				}
				if line.SessionID != "" {
					if sessionID == "" {
						emit(StreamEvent{Type: "status", Detail: "CLI session initialized.", ID: line.SessionID})
					}
					sessionID = line.SessionID
				}
				handleOpenCodeLine(line, rawMap, &acc, emit)
				if (line.Type == "error" || line.Type == "session.error") && firstErr == nil {
					firstErr = errors.New(openCodeErrorString(line.Error))
				}
			} else {
				plain.Write(trimmed)
				plain.WriteByte('\n')
			}
		}
		if err != nil {
			text := strings.TrimRight(acc.String(), "\n")
			if text == "" {
				text = strings.TrimRight(plain.String(), "\n")
			}
			if err == io.EOF {
				if activeStep && firstErr == nil {
					firstErr = errors.New("CLI stream ended before step completion")
				}
				return &Reply{Text: text, SessionID: sessionID}, firstErr
			}
			return &Reply{Text: text, SessionID: sessionID}, err
		}
	}
}

func decodeOpenCodeLine(raw map[string]any) openCodeStreamLine {
	if payload, ok := raw["payload"].(map[string]any); ok && payload["type"] != nil {
		raw = payload
	}
	if props, ok := raw["properties"].(map[string]any); ok {
		if stringFromMap(raw, "sessionID") == "" {
			if sessionID := firstMapString(props, "sessionID", "sessionId", "session_id"); sessionID != "" {
				raw["sessionID"] = sessionID
			}
		}
		if raw["part"] == nil {
			raw["part"] = props["part"]
		}
		if raw["error"] == nil {
			raw["error"] = props["error"]
		}
	}
	if stringFromMap(raw, "sessionID") == "" {
		if sessionID := firstMapString(raw, "sessionId", "session_id"); sessionID != "" {
			raw["sessionID"] = sessionID
		}
	}
	if stringFromMap(raw, "sessionID") == "" {
		if session, ok := raw["session"].(map[string]any); ok {
			if sessionID := firstMapString(session, "id", "sessionID", "sessionId", "session_id"); sessionID != "" {
				raw["sessionID"] = sessionID
			}
		}
	}
	var line openCodeStreamLine
	body, _ := json.Marshal(raw)
	_ = json.Unmarshal(body, &line)
	return line
}

func handleOpenCodeLine(line openCodeStreamLine, raw map[string]any, acc *strings.Builder, emit StreamHandler) {
	switch line.Type {
	case "text":
		text := stringFromMap(line.Part, "text")
		if text == "" {
			text = line.Delta
		}
		if text != "" {
			acc.WriteString(text)
			emit(StreamEvent{Type: "text", Text: text, Raw: raw})
		}
	case "reasoning":
		text := openCodeReasoningText(line.Part)
		if text == "" {
			text = line.Delta
		}
		if text != "" {
			emit(StreamEvent{Type: "reasoning", Text: text, Raw: raw})
		}
	case "tool_use":
		emitOpenCodeTool(line.Part, raw, emit)
	case "step_start":
		emit(StreamEvent{Type: "step_start", Detail: openCodePartDetail(line.Part), ID: stringFromMap(line.Part, "id"), Raw: raw})
	case "step_finish":
		emitOpenCodeUsage(line.Part, emit)
		emit(StreamEvent{Type: "step_finish", Detail: openCodePartDetail(line.Part), ID: stringFromMap(line.Part, "id"), OK: true, Raw: raw})
	case "message.part.delta":
		if line.Field == "text" && line.Delta != "" {
			acc.WriteString(line.Delta)
			emit(StreamEvent{Type: "text", Text: line.Delta, Raw: raw})
		} else if (line.Field == "reasoning" || line.Field == "summary") && line.Delta != "" {
			emit(StreamEvent{Type: "reasoning", Text: line.Delta, Raw: raw})
		}
	case "message.part.updated":
		handleOpenCodePartUpdated(line.Part, raw, acc, emit)
	case "error", "session.error":
		emit(StreamEvent{Type: "error", Detail: openCodeErrorString(line.Error), Raw: raw})
	}
}

func handleOpenCodePartUpdated(part map[string]any, raw map[string]any, acc *strings.Builder, emit StreamHandler) {
	switch stringFromMap(part, "type") {
	case "text":
		if text := stringFromMap(part, "text"); text != "" {
			acc.WriteString(text)
			emit(StreamEvent{Type: "text", Text: text, Raw: raw})
		}
	case "reasoning":
		if text := openCodeReasoningText(part); text != "" {
			emit(StreamEvent{Type: "reasoning", Text: text, Raw: raw})
		}
	case "tool":
		emitOpenCodeTool(part, raw, emit)
	case "step-start":
		emit(StreamEvent{Type: "step_start", Detail: openCodePartDetail(part), ID: stringFromMap(part, "id"), Raw: raw})
	case "step-finish":
		emitOpenCodeUsage(part, emit)
		emit(StreamEvent{Type: "step_finish", Detail: openCodePartDetail(part), ID: stringFromMap(part, "id"), OK: true, Raw: raw})
	}
}

func emitOpenCodeTool(part map[string]any, raw map[string]any, emit StreamHandler) {
	tool := stringFromMap(part, "tool")
	if tool == "" {
		tool = stringFromMap(part, "name")
	}
	if tool == "" {
		tool = "tool"
	}
	id := stringFromMap(part, "id")
	detail := openCodePartDetail(part)
	state, _ := part["state"].(map[string]any)
	status := stringFromMap(state, "status")
	if status == "" || status == "running" || status == "pending" {
		emit(StreamEvent{Type: "tool_start", Tool: tool, Detail: detail, ID: id, Raw: raw})
		return
	}
	// `opencode run --format json` commonly emits tool parts only once they
	// complete; synthesize a start so PrAImate's activity feed remains paired.
	emit(StreamEvent{Type: "tool_start", Tool: tool, Detail: detail, ID: id, Raw: raw})
	emit(StreamEvent{Type: "tool_end", Tool: tool, Detail: detail, ID: id, OK: status != "error" && status != "failed", Raw: raw})
}

func openCodeReasoningText(part map[string]any) string {
	for _, key := range []string{"text", "summary", "reasoning", "content"} {
		if s := stringFromMap(part, key); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func openCodePartDetail(part map[string]any) string {
	if part == nil {
		return ""
	}
	if state, _ := part["state"].(map[string]any); state != nil {
		if input, _ := state["input"].(map[string]any); input != nil {
			if detail := summarizeToolInput(input); detail != "" {
				return detail
			}
		}
		if metadata, _ := state["metadata"].(map[string]any); metadata != nil {
			if detail := summarizeToolInput(metadata); detail != "" {
				return detail
			}
		}
	}
	for _, key := range []string{"command", "path", "file", "title", "text"} {
		if s := stringFromMap(part, key); strings.TrimSpace(s) != "" {
			return truncate(strings.ReplaceAll(s, "\n", " ⏎ "), 160)
		}
	}
	return ""
}

func openCodeErrorString(v any) string {
	if v == nil {
		return "opencode error"
	}
	if s, ok := v.(string); ok {
		return s
	}
	if m, ok := v.(map[string]any); ok {
		if data, _ := m["data"].(map[string]any); data != nil {
			if msg := stringFromMap(data, "message"); msg != "" {
				if ref := stringFromMap(data, "ref"); ref != "" {
					msg += " (ref: " + truncate(ref, 100) + ")"
				}
				return msg
			}
		}
		if msg := stringFromMap(m, "message"); msg != "" {
			return msg
		}
		if name := stringFromMap(m, "name"); name != "" {
			return name
		}
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := stringFromMap(m, key); s != "" {
			return s
		}
	}
	return ""
}
