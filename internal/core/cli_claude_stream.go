package core

// Claude Code streaming — drives `claude --print --output-format
// stream-json --verbose --include-partial-messages` and translates the
// JSONL event stream into StreamEvents:
//
//   stream_event / content_block_delta / text_delta → "text" deltas
//   assistant message tool_use blocks               → "tool_start"
//   user message tool_result blocks                 → "tool_end"
//   result                                          → final Reply
//
// stream-json in print mode requires --verbose (the CLI refuses
// otherwise). --include-partial-messages adds the token-level deltas;
// when a claude version doesn't emit them, the parser falls back to the
// complete assistant text blocks so older CLIs still stream per-message.

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
	"strings"
)

func (a *ClaudeAdapter) SingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error) {
	path, err := a.resolve()
	if err != nil {
		return nil, err
	}
	args, message := a.singleShotArgs(path, "stream-json", opts)
	args = append(args, "--verbose", "--include-partial-messages")
	extra, cleanup, err := approvalArgs(opts.Tools, opts.Approval)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return a.runStream(ctx, path, opts.Cwd, opts.Env, append(args, extra...), message, emit)
}

func (a *ClaudeAdapter) ResumeStream(ctx context.Context, sessionID string, opts ResumeOpts, emit StreamHandler) (*Reply, error) {
	if sessionID == "" {
		return nil, errors.New("claude.ResumeStream: empty sessionID")
	}
	path, err := a.resolve()
	if err != nil {
		return nil, err
	}
	args := append(resumeArgs("stream-json", sessionID, opts), "--verbose", "--include-partial-messages")
	extra, cleanup, err := approvalArgs(opts.Tools, opts.Approval)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return a.runStream(ctx, path, opts.Cwd, opts.Env, append(args, extra...), opts.Message, emit)
}

// runStream is runAt's streaming sibling: stdout is consumed line by
// line as the CLI emits events instead of buffered to completion.
func (a *ClaudeAdapter) runStream(ctx context.Context, path, cwd string, env map[string]string, args []string, message string, emit StreamHandler) (*Reply, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = mergeEnv(os.Environ(), env)
	cmd.Stdin = strings.NewReader(message)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude stream: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude stream: start: %w", err)
	}

	reply, parseErr := parseClaudeStream(stdout, emit)

	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			exitCode = ee.ExitCode()
		} else if ctx.Err() == nil {
			return nil, fmt.Errorf("claude stream: %w (stderr=%s)", waitErr, truncate(stderr.String(), 400))
		}
	}
	if ctx.Err() != nil {
		// Interrupted — hand back whatever streamed so the caller can
		// persist the partial reply.
		if reply == nil {
			reply = &Reply{}
		}
		reply.ExitCode = exitCode
		return reply, ctx.Err()
	}
	if reply == nil || (parseErr != nil && exitCode != 0 && reply.Text == "") {
		// Crashed before producing anything useful (e.g. an old CLI
		// rejecting a flag) — surface the CLI's own stderr.
		return nil, fmt.Errorf("claude stream: %v (stderr=%s)", firstErr(parseErr, errors.New("no result event")), truncate(stderr.String(), 400))
	}
	reply.ExitCode = exitCode
	return reply, nil
}

// firstErr returns the first non-nil error.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// claudeStreamLine is the tolerant superset of the stream-json line
// shapes we care about; unknown types fall through silently.
type claudeStreamLine struct {
	Type      string         `json:"type"`
	SessionID string         `json:"session_id"`
	Result    string         `json:"result"`
	IsError   bool           `json:"is_error"`
	Usage     map[string]any `json:"usage"`
	Event     *struct {
		Type  string `json:"type"`
		Delta *struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
	Message *struct {
		ID      string               `json:"id"`
		Usage   map[string]any       `json:"usage"`
		Content []claudeContentBlock `json:"content"`
		Model   string               `json:"model"`
	} `json:"message"`
}

type claudeContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	IsError   bool           `json:"is_error"`
}

// parseClaudeStream consumes the JSONL stream, emitting StreamEvents
// and returning the final Reply once the "result" line arrives. If the
// stream ends without a result (crash / interrupt), it returns a Reply
// assembled from the accumulated text and a non-nil error.
func parseClaudeStream(r io.Reader, emit StreamHandler) (*Reply, error) {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	// Tool-result lines can be huge (whole file reads) — use a Reader,
	// not a Scanner with its default 64KB token cap.
	br := bufio.NewReaderSize(r, 256*1024)
	var (
		acc       strings.Builder // accumulated text (fallback + interrupt)
		sessionID string
		sawDelta  bool
	)
	// Retain reported usage when cancellation/crashes omit the final aggregate.
	partialUsage := map[string]*NativeUsage{}
	finalUsage := false
	defer func() {
		if !finalUsage {
			for id, usage := range partialUsage {
				emit(StreamEvent{Type: "usage", ID: id, Usage: usage, OK: true})
			}
		}
	}()
	for {
		raw, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var line claudeStreamLine
			if jerr := json.Unmarshal(bytes.TrimSpace(raw), &line); jerr == nil {
				if line.SessionID != "" {
					sessionID = line.SessionID
				}
				switch line.Type {
				case "stream_event":
					if line.Event != nil && line.Event.Delta != nil && line.Event.Delta.Type == "text_delta" && line.Event.Delta.Text != "" {
						sawDelta = true
						acc.WriteString(line.Event.Delta.Text)
						emit(StreamEvent{Type: "text", Text: line.Event.Delta.Text})
					}
				case "assistant":
					if line.Message != nil {
						if usage := claudeUsage(line.Message.Usage); usage != nil && line.Message.ID != "" {
							partialUsage[line.Message.ID] = usage
						}
						if line.Message.Model != "" {
							emit(StreamEvent{Type: "model", Model: line.Message.Model})
						}
						for _, b := range line.Message.Content {
							switch b.Type {
							case "tool_use":
								emit(StreamEvent{Type: "tool_start", Tool: b.Name, Detail: summarizeToolInput(b.Input), ID: b.ID})
							case "text":
								// Older CLIs without partial messages:
								// stream per complete assistant block.
								if !sawDelta && b.Text != "" {
									acc.WriteString(b.Text)
									emit(StreamEvent{Type: "text", Text: b.Text})
								}
							}
						}
					}
				case "user":
					if line.Message != nil {
						for _, b := range line.Message.Content {
							if b.Type == "tool_result" {
								emit(StreamEvent{Type: "tool_end", ID: b.ToolUseID, OK: !b.IsError})
							}
						}
					}
				case "result":
					if usage := claudeUsage(line.Usage); usage != nil {
						finalUsage = true
						emit(StreamEvent{Type: "usage", Usage: usage, OK: true})
					}
					return &Reply{
						Text:      strings.TrimRight(line.Result, "\n"),
						SessionID: sessionID,
					}, nil
				}
			}
		}
		if err != nil {
			partial := &Reply{Text: strings.TrimRight(acc.String(), "\n"), SessionID: sessionID}
			if err == io.EOF {
				return partial, errors.New("stream ended before result event")
			}
			return partial, err
		}
	}
}
