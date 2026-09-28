package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Request contains only the task context explicitly selected for a worker.
// A worker never receives the primary chat history through this contract.
type Request struct {
	Model         string
	SystemPrompt  string
	Task          string
	WorkspaceRoot string
	Limits        Limits
	Progress      func(ProgressEvent)
}

type ProgressEvent struct {
	Kind string
	Text string
}

type Limits struct {
	MaxInputBytes   int
	MaxOutputTokens int
	Timeout         time.Duration
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	Source       string // "provider" or "unavailable"
}

type Result struct {
	Content      string
	FinishReason string
	Usage        Usage
}

type Capabilities struct {
	OutputTokenLimit bool
	ProviderUsage    bool
	ReadOnly         bool
	CanEdit          bool
}

type Runtime interface {
	ID() string
	Capabilities() Capabilities
	Execute(context.Context, Request) (*Result, error)
}

func validate(req Request) error {
	if strings.TrimSpace(req.Task) == "" {
		return errors.New("worker task is required")
	}
	if req.WorkspaceRoot == "" || !filepath.IsAbs(req.WorkspaceRoot) {
		return errors.New("worker workspace must be an absolute path")
	}
	info, err := os.Stat(req.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("worker workspace: %w", err)
	}
	if !info.IsDir() {
		return errors.New("worker workspace must be a directory")
	}
	if req.Limits.MaxInputBytes < 0 || req.Limits.MaxOutputTokens < 0 || req.Limits.Timeout < 0 {
		return errors.New("worker limits cannot be negative")
	}
	if req.Limits.MaxInputBytes > 0 && len(req.SystemPrompt)+len(req.Task) > req.Limits.MaxInputBytes {
		return errors.New("worker input exceeds byte limit")
	}
	return nil
}

func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}
