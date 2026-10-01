package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
	"strings"
	"time"
)

const assistantTasksKey = "application_assistant_tasks_v1"

type applicationTask struct {
	ID      string            `json:"id"`
	Goal    string            `json:"goal"`
	Tier    orchestrator.Tier `json:"tier"`
	Status  string            `json:"status"`
	RunID   string            `json:"run_id,omitempty"`
	Updated time.Time         `json:"updated_at"`
}

func (a *App) loadApplicationTasks(ctx context.Context) ([]applicationTask, error) {
	raw, err := a.core.GetSetting(ctx, core.ScopeGUI, assistantTasksKey)
	if err != nil {
		return nil, err
	}
	tasks := []applicationTask{}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &tasks)
	}
	return tasks, err
}
func (a *App) saveApplicationTasks(ctx context.Context, tasks []applicationTask) error {
	raw, err := json.Marshal(tasks)
	if err != nil {
		return err
	}
	return a.core.SetSetting(ctx, core.ScopeGUI, assistantTasksKey, raw)
}
func (a *App) registerAssistantTasks(r *assistant.Registry) {
	f := func(desc string, required bool) assistant.Field {
		return assistant.Field{Type: "string", Description: desc, Required: required}
	}
	r.Register(assistant.Action{Name: "tasks.create", Description: "Save a concrete task for later execution by primary, middle or fast. Does not start any model.", Capability: "tasks", Fields: map[string]assistant.Field{"goal": f("Task goal, constraints and expected output", true), "tier": f("primary, middle or fast", true)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		goal := strings.TrimSpace(textArg(args, "goal"))
		tier := orchestrator.Tier(textArg(args, "tier"))
		if goal == "" || len(goal) > 16384 || (tier != orchestrator.Primary && tier != orchestrator.Middle && tier != orchestrator.Fast) {
			return nil, errors.New("invalid task goal or tier")
		}
		a.assistantTasksMu.Lock()
		defer a.assistantTasksMu.Unlock()
		tasks, err := a.loadApplicationTasks(ctx)
		if err != nil {
			return nil, err
		}
		if len(tasks) >= 100 {
			return nil, errors.New("saved task limit reached; delete completed tasks first")
		}
		task := applicationTask{ID: fmt.Sprintf("task-%d", time.Now().UnixNano()), Goal: goal, Tier: tier, Status: "prepared", Updated: time.Now().UTC()}
		tasks = append(tasks, task)
		if err := a.saveApplicationTasks(ctx, tasks); err != nil {
			return nil, err
		}
		return task, nil
	}})
	r.Register(assistant.Action{Name: "tasks.list", Description: "List saved assistant task goals and linked worker executions", Capability: "read", Execute: func(ctx context.Context, args map[string]any) (any, error) {
		a.assistantTasksMu.Lock()
		defer a.assistantTasksMu.Unlock()
		return a.loadApplicationTasks(ctx)
	}})
	r.Register(assistant.Action{Name: "tasks.inspect", Description: "Inspect a saved task and live status/output of its linked worker execution", Capability: "read", Fields: map[string]assistant.Field{"id": f("Task ID", true)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		a.assistantTasksMu.Lock()
		defer a.assistantTasksMu.Unlock()
		tasks, err := a.loadApplicationTasks(ctx)
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			if task.ID == textArg(args, "id") {
				result := map[string]any{"task": task}
				if task.RunID != "" {
					run, err := a.WorkerRunSnapshot(task.RunID)
					if err != nil {
						return nil, err
					}
					result["status"] = run.Status
					result["result"] = run.Result
					result["error"] = run.Error
				}
				return result, nil
			}
		}
		return nil, errors.New("task not found")
	}})
	r.Register(assistant.Action{Name: "tasks.run", Description: "Start a prepared task using its saved worker tier. Starting/active tasks are never automatically replayed.", Capability: "delegate", ExtraCapabilities: func(map[string]any) []string { return []string{"tasks"} }, Fields: map[string]assistant.Field{"id": f("Prepared task ID", true)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		if a.workers == nil {
			return nil, errors.New("worker runtime unavailable")
		}
		a.assistantTasksMu.Lock()
		defer a.assistantTasksMu.Unlock()
		tasks, err := a.loadApplicationTasks(ctx)
		if err != nil {
			return nil, err
		}
		for i := range tasks {
			task := &tasks[i]
			if task.ID != textArg(args, "id") {
				continue
			}
			if task.Status != "prepared" {
				return nil, errors.New("task already started; inspect its worker execution before retrying")
			}
			task.Status = "starting"
			task.Updated = time.Now().UTC()
			if err := a.saveApplicationTasks(ctx, tasks); err != nil {
				return nil, err
			}
			id, err := a.workers.StartAtTier(task.Goal, task.Tier, a.approvalProvider)
			if err != nil {
				task.Status = "prepared"
				if saveErr := a.saveApplicationTasks(context.Background(), tasks); saveErr != nil {
					return nil, fmt.Errorf("start failed and task checkpoint failed: %w", saveErr)
				}
				return nil, err
			}
			task.RunID = id
			task.Status = "started"
			if err := a.saveApplicationTasks(context.Background(), tasks); err != nil {
				return nil, fmt.Errorf("worker %s started but task checkpoint failed; inspect Workers before retrying: %w", id, err)
			}
			return map[string]any{"task_id": task.ID, "run_id": id, "status": "started"}, nil
		}
		return nil, errors.New("task not found")
	}})
	r.Register(assistant.Action{Name: "tasks.cancel", Description: "Cancel a linked worker execution or a prepared task", Capability: "tasks", Fields: map[string]assistant.Field{"id": f("Task ID", true)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		a.assistantTasksMu.Lock()
		defer a.assistantTasksMu.Unlock()
		tasks, err := a.loadApplicationTasks(ctx)
		if err != nil {
			return nil, err
		}
		for i := range tasks {
			if tasks[i].ID == textArg(args, "id") {
				if tasks[i].RunID != "" {
					if err := a.CancelWorkerRun(tasks[i].RunID); err != nil {
						return nil, err
					}
				}
				tasks[i].Status = "cancelled"
				tasks[i].Updated = time.Now().UTC()
				if err := a.saveApplicationTasks(ctx, tasks); err != nil {
					return nil, err
				}
				return tasks[i], nil
			}
		}
		return nil, errors.New("task not found")
	}})
	r.Register(assistant.Action{Name: "tasks.delete", Description: "Remove a saved task record after its worker stops. Worker history is retained.", Capability: "tasks", Fields: map[string]assistant.Field{"id": f("Task ID", true)}, Execute: func(ctx context.Context, args map[string]any) (any, error) {
		a.assistantTasksMu.Lock()
		defer a.assistantTasksMu.Unlock()
		tasks, err := a.loadApplicationTasks(ctx)
		if err != nil {
			return nil, err
		}
		for i, task := range tasks {
			if task.ID != textArg(args, "id") {
				continue
			}
			if task.Status == "starting" {
				return nil, errors.New("task start status is uncertain; inspect Workers before removing it")
			}
			if task.RunID != "" {
				run, err := a.WorkerRunSnapshot(task.RunID)
				if err != nil {
					return nil, err
				}
				if run.Status == "running" {
					return nil, errors.New("cancel the active task before deleting its record")
				}
			}
			tasks = append(tasks[:i], tasks[i+1:]...)
			if err := a.saveApplicationTasks(ctx, tasks); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true}, nil
		}
		return nil, errors.New("task not found")
	}})
}
