package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
)

func runWorkers(args []string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(os.Stderr, "usage: praimate workers plan|list|show|execute|review|merge|reset|cleanup [--id RUN] [options]")
		if len(args) > 0 {
			return 0
		}
		return 2
	}
	action := args[0]
	f := flag.NewFlagSet("workers "+action, flag.ContinueOnError)
	id := f.String("id", "", "saved worker run ID")
	taskID := f.String("task-id", "", "task to review or reset")
	objective := f.String("task", "", "objective for the coordinator")
	configPath := f.String("config", "", "existing worker Config JSON (otherwise saved defaults)")
	planPath := f.String("plan", "", "reviewed tasks JSON array to save before execution")
	workspace := f.String("workspace", "", "Git repository root (plan only)")
	parallel := f.Int("parallel", 2, "maximum concurrent tasks (1–4)")
	decision := f.String("decision", "", "accepted or rejected")
	discard := f.Bool("discard", false, "explicitly discard failed work or reviewed temporary worktrees")
	approve := f.Bool("approve-tools", false, "approve host-brokered commands and writes inside task worktrees")
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	c, closeCore, err := openCore()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeCore()
	manager := orchestrator.NewManager(c, ctx)
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = manager.Stop(stop)
	}()
	readJSON := func(path string, value any) error {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		decoder := json.NewDecoder(io.LimitReader(file, 128<<10))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(value); err != nil {
			return err
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("expected one JSON value")
		}
		return nil
	}
	wait := func(runID string) (orchestrator.Run, error) {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			run, err := manager.Snapshot(runID)
			if err != nil {
				return run, err
			}
			if run.Status != "planning" && run.Status != "running" && run.Status != "merging" {
				return run, nil
			}
			select {
			case <-ctx.Done():
				_ = manager.Cancel(runID)
				return run, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	var output any
	switch action {
	case "list":
		output, err = manager.List()
	case "plan":
		var config orchestrator.Config
		if *configPath != "" {
			err = readJSON(*configPath, &config)
		} else {
			config, err = orchestrator.LoadConfig(ctx, c)
		}
		if *workspace != "" {
			config.Workspace = *workspace
		}
		var runID string
		if err == nil {
			runID, err = manager.PlanDAG(*objective, config, *parallel)
		}
		if err == nil {
			output, err = wait(runID)
		}
	case "show":
		output, err = manager.Snapshot(*id)
	case "execute":
		if *planPath != "" {
			var tasks []orchestrator.DAGTask
			err = readJSON(*planPath, &tasks)
			if err == nil {
				err = manager.UpdateDAG(*id, tasks, *parallel)
			}
		}
		var provider func(string) *core.ApprovalConfig
		if *approve {
			provider = func(string) *core.ApprovalConfig {
				return &core.ApprovalConfig{Request: func(context.Context, string, map[string]any) (bool, error) { return true, nil }}
			}
		}
		if err == nil {
			err = manager.ExecuteDAG(*id, provider)
		}
		if err == nil {
			output, err = wait(*id)
		}
	case "review":
		err = manager.ReviewDAGTask(*id, *taskID, *decision)
	case "merge":
		err = manager.MergeDAG(*id)
		if err == nil {
			output, err = wait(*id)
		}
	case "reset":
		if !*discard {
			err = fmt.Errorf("reset removes failed task work; pass --discard after inspecting its worktree")
		} else {
			err = manager.ResetDAGTask(*id, *taskID)
		}
	case "cleanup":
		if !*discard {
			err = fmt.Errorf("cleanup removes reviewed temporary worktrees; pass --discard")
		} else {
			err = manager.CleanupDAG(*id)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown workers action:", action)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if output == nil {
		output, err = manager.Snapshot(*id)
	}
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(output)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if run, ok := output.(orchestrator.Run); ok && (run.Status == "failed" || run.Status == "cancelled") {
		return 1
	}
	return 0
}
