package runtime

import (
	"context"
	"errors"
	"fmt"

	"git.jtsec.local/lab/PrAImate/internal/core"
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
	editSupported := c.Adapter.Name() == "codex" || c.Adapter.Name() == "claude" || c.Adapter.Name() == "openclaude"
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
		Model: req.Model, Tools: func() string {
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
	if stream, ok := c.Adapter.(interface {
		SingleShotStream(context.Context, core.SingleShotOpts, core.StreamHandler) (*core.Reply, error)
	}); ok && req.Progress != nil {
		reply, err = stream.SingleShotStream(ctx, opts, func(event core.StreamEvent) {
			if event.Type == "text" && event.Text != "" {
				req.Progress(ProgressEvent{Kind: "stream", Text: event.Text})
			} else if event.Type == "tool_start" {
				req.Progress(ProgressEvent{Kind: "tool", Text: event.Tool + " " + event.Detail})
			}
		})
		if errors.Is(err, core.ErrStreamUnsupported) {
			reply, err = c.Adapter.SingleShot(ctx, opts)
		}
	} else {
		reply, err = c.Adapter.SingleShot(ctx, opts)
	}
	if err != nil {
		return nil, err
	}
	if reply == nil || reply.ExitCode != 0 {
		return nil, fmt.Errorf("CLI worker %q failed", c.Adapter.Name())
	}
	return &Result{Content: reply.Text, FinishReason: "stop", Usage: Usage{Source: "unavailable"}}, nil
}
