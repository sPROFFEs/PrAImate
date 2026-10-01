package core

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
	"runtime"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

// additionalCLIAdapter implements the official Copilot and Antigravity JSONL
// protocols. Prompts travel over stdin, including on Windows npm shims.
type additionalCLIAdapter struct{ *execAdapter }

func NewCopilotAdapter() *additionalCLIAdapter {
	return &additionalCLIAdapter{&execAdapter{name: "copilot", bin: "copilot", managedSafe: true, extraDirs: launcher.CopilotNativeBinDirs()}}
}

func NewAntigravityAdapter() *additionalCLIAdapter {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "bin")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin")
	}
	// Antigravity's plan mode is an instruction prefix, not an enforced
	// read-only tool policy. Do not advertise it to the managed worker runtime.
	return &additionalCLIAdapter{&execAdapter{name: "antigravity", bin: "agy", extraDirs: []string{dir}}}
}

func (a *additionalCLIAdapter) SupportsResume() bool { return true }
func (a *additionalCLIAdapter) SingleShot(ctx context.Context, o SingleShotOpts) (*Reply, error) {
	return a.SingleShotStream(ctx, o, nil)
}
func (a *additionalCLIAdapter) Resume(ctx context.Context, id string, o ResumeOpts) (*Reply, error) {
	return a.ResumeStream(ctx, id, o, nil)
}
func (a *additionalCLIAdapter) SingleShotStream(ctx context.Context, o SingleShotOpts, emit StreamHandler) (*Reply, error) {
	if s := strings.TrimSpace(o.SystemPrompt); s != "" {
		o.Message = s + "\n\n" + o.Message
	}
	return a.run(ctx, "", o, emit)
}
func (a *additionalCLIAdapter) ResumeStream(ctx context.Context, id string, o ResumeOpts, emit StreamHandler) (*Reply, error) {
	if strings.TrimSpace(id) == "" || strings.HasPrefix(id, "-") || strings.ContainsAny(id, "\x00\r\n") {
		return nil, errors.New("invalid CLI session ID")
	}
	return a.run(ctx, id, SingleShotOpts{Cwd: o.Cwd, Message: o.Message, Model: o.Model, Tools: o.Tools, Env: o.Env}, emit)
}

func additionalCLIArgs(cli, model, level, session string) ([]string, error) {
	if _, _, err := InteractiveCLICommand(cli, model); err != nil {
		return nil, err
	}
	var args []string
	if cli == "copilot" {
		args = []string{"--output-format=json", "--stream=on", "--no-ask-user"}
		switch level {
		case "full":
			args = append(args, "--allow-all")
		case "edits":
			args = append(args, "--available-tools=view,glob,grep,edit,create", "--allow-tool=read,write", "--disable-builtin-mcps")
		default:
			args = append(args, "--available-tools=view,glob,grep", "--allow-tool=read", "--disable-builtin-mcps")
		}
		if session != "" {
			args = append(args, "--resume="+session)
		}
	} else {
		args = []string{"--input-format", "stream-json", "--output-format", "stream-json"}
		switch level {
		case "full":
			args = append(args, "--dangerously-skip-permissions")
		case "edits":
			args = append(args, "--mode=accept-edits")
		case "plan":
			args = append(args, "--mode=plan")
		default:
			args = append(args, "--mode=default")
		}
		if session != "" {
			args = append(args, "--conversation", session)
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args, nil
}

func (a *additionalCLIAdapter) run(ctx context.Context, session string, o SingleShotOpts, emit StreamHandler) (*Reply, error) {
	path, err := a.resolveBin()
	if err != nil {
		return nil, fmt.Errorf("%s executable not found; install it from the CLIs tab", a.name)
	}
	args, err := additionalCLIArgs(a.name, o.Model, o.Tools, session)
	if err != nil {
		return nil, err
	}
	extra, cleanup, err := PrepareInteractiveCLIConfig(a.name, o.Env)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	args = append(args, extra...)
	var telemetry *TerminalUsage
	var streamUsage []StreamEvent
	handler := emit
	if handler == nil {
		handler = func(StreamEvent) {}
	}
	if a.name == "copilot" {
		// The CLI's JSONL filters assistant.usage; unlike its SDK, it only
		// includes non-token billing stats in the final result. Capture per-call
		// OTLP reports for this process, including resumed conversations.
		telemetry, err = beginUsageReceiver(context.Background(), nil, a.name, o.Model, o.Env)
		if err != nil {
			return nil, fmt.Errorf("prepare copilot usage: %w", err)
		}
		defer telemetry.Close()
		launchEnv := make(map[string]string, len(o.Env)+len(telemetry.Env))
		for k, v := range o.Env {
			launchEnv[k] = v
		}
		for k, v := range telemetry.Env {
			launchEnv[k] = v
		}
		o.Env = launchEnv
	}
	message := o.Message
	if a.name == "antigravity" {
		payload, _ := json.Marshal(map[string]any{"event": "user", "message": map[string]string{"content": message}})
		message = string(payload) + "\n"
	}
	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	cmd.Dir = o.Cwd
	cmd.Env = mergeEnv(os.Environ(), o.Env)
	cmd.Stdin = strings.NewReader(message)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %w", a.name, err)
	}
	reply, parseErr := parseAdditionalCLIStream(stdout, a.name, func(e StreamEvent) {
		if telemetry != nil && e.Type == "usage" {
			streamUsage = append(streamUsage, e)
			return
		}
		handler(e)
	})
	waitErr := cmd.Wait()
	if telemetry != nil {
		// Wait for the CLI's exporter shutdown before closing the receiver.
		telemetry.Close()
		telemetry.captureMu.Lock()
		captured := append([]StreamEvent(nil), telemetry.captured...)
		telemetry.captureMu.Unlock()
		if len(captured) == 0 {
			captured = streamUsage
		}
		for _, event := range captured {
			handler(event)
		}
	}
	if reply.SessionID == "" {
		reply.SessionID = session
	}
	if waitErr != nil {
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			reply.ExitCode = exit.ExitCode()
		}
	}
	if ctx.Err() != nil {
		return reply, ctx.Err()
	}
	if waitErr != nil {
		return reply, fmt.Errorf("%s: %w (%s)", a.name, waitErr, truncate(stderr.String(), 400))
	}
	return reply, parseErr
}

// Count completed steps in Antigravity, never its cumulative session result.
// Copilot usage events are per API call; event IDs prevent stream duplicates.
func parseAdditionalCLIStream(r io.Reader, cli string, emit StreamHandler) (*Reply, error) {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	br := bufio.NewReaderSize(r, 64<<10)
	reply := &Reply{}
	var acc strings.Builder
	deltas := map[string]bool{}
	seen := map[string]bool{}
	completed := false
	var streamErr error
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e map[string]any
			if json.Unmarshal(line, &e) == nil {
				typ := stringFromMap(e, "type")
				d, _ := e["data"].(map[string]any)
				id := stringFromMap(e, "id")
				if cli == "antigravity" {
					typ = stringFromMap(e, "event")
					d, _ = e[typ].(map[string]any)
				}
				if id != "" && seen[id] {
					if err != nil {
						break
					}
					continue
				}
				if id != "" {
					seen[id] = true
				}
				text := ""
				switch typ {
				case "session.start", "session.resume":
					if s := stringFromMap(d, "sessionId"); s != "" {
						reply.SessionID = s
					}
					emit(StreamEvent{Type: "model", Model: stringFromMap(d, "selectedModel")})
				case "assistant.message_delta":
					if stringFromMap(e, "agentId") == "" {
						text = stringFromMap(d, "deltaContent")
						deltas[stringFromMap(d, "messageId")] = true
					}
				case "assistant.message":
					if stringFromMap(e, "agentId") == "" && !deltas[stringFromMap(d, "messageId")] {
						text = stringFromMap(d, "content")
					}
				case "assistant.usage":
					if usage := reportedUsage(d, "inputTokens", "outputTokens"); usage != nil {
						emit(StreamEvent{Type: "usage", ID: id, Model: stringFromMap(d, "model"), Usage: usage, OK: true})
					}
				case "tool.execution_start":
					input, _ := d["arguments"].(map[string]any)
					emit(StreamEvent{Type: "tool_start", ID: stringFromMap(d, "toolCallId"), Tool: stringFromMap(d, "toolName"), Detail: summarizeToolInput(input)})
				case "tool.execution_complete":
					ok, _ := d["success"].(bool)
					emit(StreamEvent{Type: "tool_end", ID: stringFromMap(d, "toolCallId"), OK: ok})
				case "session.idle":
					completed = true
				case "session.error":
					streamErr = errors.New(stringFromMap(d, "message"))
				case "init":
					reply.SessionID = stringFromMap(e, "conversation_id")
					emit(StreamEvent{Type: "model", Model: stringFromMap(d, "model")})
				case "step_update":
					stepID := fmt.Sprint(d["step_index"])
					if stringFromMap(d, "step_type") == "agent_response" {
						text = stringFromMap(d, "text_delta")
					}
					if stringFromMap(d, "state") == "DONE" {
						fields, _ := d["usage"].(map[string]any)
						if usage := reportedUsage(fields, "input_tokens", "output_tokens"); usage != nil {
							emit(StreamEvent{Type: "usage", ID: stepID, Usage: usage, OK: true})
						}
					}
					if stringFromMap(d, "step_type") == "tool" {
						info, _ := d["tool_info"].(map[string]any)
						input, _ := info["parameters"].(map[string]any)
						typeName := "tool_start"
						if stringFromMap(d, "state") == "DONE" {
							typeName = "tool_end"
						}
						emit(StreamEvent{Type: typeName, ID: stepID, Tool: stringFromMap(d, "tool_name"), Detail: summarizeToolInput(input), OK: info["error"] == nil})
					}
				case "result":
					completed = true
					if cli == "copilot" {
						reply.SessionID = stringFromMap(e, "sessionId")
						code, ok := e["exitCode"].(float64)
						if !ok || code != 0 {
							reply.ExitCode = int(code)
							streamErr = fmt.Errorf("copilot run did not complete successfully (exit %d, %s)", reply.ExitCode, stringFromMap(e, "outcome"))
						}
						break
					}
					if s := stringFromMap(d, "conversation_id"); s != "" {
						reply.SessionID = s
					}
					if s := stringFromMap(d, "response"); s != "" {
						reply.Text = s
						if acc.Len() == 0 {
							text = s
						}
					}
					if status := stringFromMap(d, "status"); status != "SUCCESS" {
						streamErr = fmt.Errorf("antigravity %s: %s", status, stringFromMap(d, "error"))
					}
				}
				if text != "" {
					acc.WriteString(text)
					emit(StreamEvent{Type: "text", Text: text})
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				streamErr = err
			}
			break
		}
	}
	if reply.Text == "" {
		reply.Text = acc.String()
	}
	if streamErr == nil && !completed {
		streamErr = errors.New("CLI stream ended without a completion event")
	}
	return reply, streamErr
}
