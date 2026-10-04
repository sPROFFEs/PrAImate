package orchestrator

import (
	"context"
	"errors"
	"fmt"

	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
)

func (r Runner) traced(config Config, profile Profile, phase string) Runner {
	id, _ := newRunID()
	r.trace = Event{WorkerID: id, ParentID: r.trace.WorkerID, Tier: profile.Tier,
		Runtime: profile.Runtime, CLI: profile.CLI, Model: profile.Model, ReasoningEffort: profile.ReasoningEffort,
		Workspace: config.Workspace, Phase: phase, TimeoutSeconds: profile.TimeoutSeconds}
	return r
}

func (r Runner) finish(profile Profile, err error) {
	if err == nil {
		r.trace.Phase = "completed"
		r.emit(profile.Tier, "completed", "Worker completed its assignment.", workerruntime.Usage{})
		return
	}
	r.trace.Phase = "failed"
	if errors.Is(err, context.Canceled) {
		r.trace.Phase = "cancelled"
	}
	r.emit(profile.Tier, r.trace.Phase, err.Error(), workerruntime.Usage{})
}

func workerError(ctx context.Context, profile Profile, phase string, err error) error {
	backend := profile.CLI
	if profile.Runtime == "native" {
		backend = "local/API"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if ctx.Err() != nil {
			return fmt.Errorf("%s worker (%s / %s): parent execution deadline expired during %s: %w", profile.Tier, backend, profile.Model, phase, err)
		}
		if profile.TimeoutSeconds == 0 {
			return fmt.Errorf("%s worker (%s / %s): a request deadline expired during %s while the host call timeout is disabled. Inspect backend timeout settings and partial activity before retrying: %w", profile.Tier, backend, profile.Model, phase, err)
		}
		return fmt.Errorf("%s worker (%s / %s): deadline expired during %s (configured call timeout: %ds). Review partial activity and timeout settings before retrying: %w", profile.Tier, backend, profile.Model, phase, profile.TimeoutSeconds, err)
	}
	return fmt.Errorf("%s worker (%s / %s), %s: %w", profile.Tier, backend, profile.Model, phase, err)
}
