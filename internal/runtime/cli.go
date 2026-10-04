package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// CLI runs one independent task through an existing headless adapter.
// It refuses adapters that cannot enforce PrAImate's read-only safe mode.
type CLI struct {
	Adapter    core.CLIAdapter
	AllowEdits bool
}

func (c CLI) ID() string {
	if c.Adapter == nil {
		return "cli"
	}
	return "cli:" + c.Adapter.Name()
}

func (c CLI) Capabilities() Capabilities {
	safe, ok := c.Adapter.(interface{ ManagedSafeMode() bool })
	managed := ok && safe.ManagedSafeMode()
	if c.Adapter == nil {
		return Capabilities{}
	}
	editSupported := c.Adapter.Name() == "codex" || c.Adapter.Name() == "claude" || c.Adapter.Name() == "openclaude" || c.Adapter.Name() == "copilot"
	return Capabilities{ReadOnly: managed && !c.AllowEdits, CanEdit: managed && c.AllowEdits && editSupported}
}

func (c CLI) Execute(ctx context.Context, req Request) (*Result, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	if req.Limits.MaxOutputTokens != 0 {
		return nil, errors.New("CLI worker cannot enforce an output token limit")
	}
	if c.Adapter == nil {
		return nil, errors.New("CLI worker adapter is required")
	}
	cap := c.Capabilities()
	if !cap.ReadOnly && !cap.CanEdit {
		return nil, fmt.Errorf("CLI worker %q cannot enforce the selected permission mode", c.Adapter.Name())
	}
	ctx, cancel := boundedContext(ctx, req.Limits.Timeout)
	defer cancel()
	opts := core.SingleShotOpts{
		Cwd: req.WorkspaceRoot, Message: req.Task, SystemPrompt: req.SystemPrompt,
		Model: req.Model, ReasoningEffort: req.ReasoningEffort, Tools: func() string {
			if c.AllowEdits {
				return "edits"
			}
			if c.Adapter.Name() == "opencode" || c.Adapter.Name() == "praimate-code" {
				return "plan"
			}
			return ""
		}(),
	}
	var reply *core.Reply
	var err error
	var usage core.UsageAccumulator
	var reportedModel string
	tools := map[string]core.StreamEvent{}
	if stream, ok := c.Adapter.(interface {
		SingleShotStream(context.Context, core.SingleShotOpts, core.StreamHandler) (*core.Reply, error)
	}); ok {
		reply, err = stream.SingleShotStream(ctx, opts, func(event core.StreamEvent) {
			usage.Observe(event)
			if req.Progress == nil {
				return
			}
			if event.Type == "text" && event.Text != "" {
				req.Progress(ProgressEvent{Kind: "stream", Text: event.Text})
			} else if event.Type == "status" {
				req.Progress(ProgressEvent{Kind: "backend_status", Text: event.Detail})
			} else if event.Type == "model" && event.Model != "" && event.Model != reportedModel {
				reportedModel = event.Model
				req.Progress(ProgressEvent{Kind: "backend_status", Text: "Backend reports model: " + event.Model})
			} else if event.Type == "step_start" || event.Type == "step_finish" {
				label := "Backend step started"
				if event.Type == "step_finish" {
					label = "Backend step finished"
				}
				req.Progress(ProgressEvent{Kind: "backend_status", Text: label + ": " + event.Detail})
			} else if event.Type == "reasoning" && event.Text != "" {
				req.Progress(ProgressEvent{Kind: "reasoning", Text: event.Text})
			} else if event.Type == "tool_start" {
				if event.ID != "" {
					// Keep only the label, not raw tool arguments or output.
					tools[event.ID] = core.StreamEvent{Tool: event.Tool, Detail: event.Detail}
				}
				req.Progress(ProgressEvent{Kind: "tool_start", Text: event.Tool + " " + event.Detail})
			} else if event.Type == "tool_end" {
				if start, ok := tools[event.ID]; ok {
					if event.Tool == "" {
						event.Tool = start.Tool
					}
					if event.Detail == "" {
						event.Detail = start.Detail
					}
					delete(tools, event.ID)
				}
				req.Progress(ProgressEvent{Kind: "tool_end", Text: fmt.Sprintf("%s %s (ok=%t)", event.Tool, event.Detail, event.OK)})
			} else if event.Type == "error" {
				req.Progress(ProgressEvent{Kind: "error", Text: event.Detail})
			}
		})
		if errors.Is(err, core.ErrStreamUnsupported) {
			reply, err = c.Adapter.SingleShot(ctx, opts)
		}
	} else {
		reply, err = c.Adapter.SingleShot(ctx, opts)
	}
	result := &Result{FinishReason: "stop", Usage: Usage{Source: "unavailable"}}
	input, output, calls, _ := usage.Snapshot()
	if calls > 0 {
		result.Usage = Usage{InputTokens: int(input), OutputTokens: int(output), Source: "provider"}
	}
	if reply != nil {
		result.Content = reply.Text
	}
	if err != nil {
		return result, err
	}
	if reply == nil {
		return result, fmt.Errorf("CLI worker %q returned no reply", c.Adapter.Name())
	}
	if reply.ExitCode != 0 {
		detail := result.Content
		if len(detail) > 1200 {
			detail = detail[:1200] + "…"
		}
		return result, fmt.Errorf("CLI worker %q failed (exit code %d): %s", c.Adapter.Name(), reply.ExitCode, detail)
	}
	return result, nil
}
