package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
)

func (a *App) WorkerConfig() (orchestrator.Config, error) {
	if a.core == nil {
		return orchestrator.Config{}, errors.New("database is unavailable")
	}
	return orchestrator.LoadConfig(context.Background(), a.core)
}

func (a *App) SaveWorkerConfig(body string) error {
	if a.core == nil {
		return errors.New("database is unavailable")
	}
	var config orchestrator.Config
	if err := json.Unmarshal([]byte(body), &config); err != nil {
		return err
	}
	return orchestrator.SaveConfig(context.Background(), a.core, config)
}

func (a *App) StartWorkerRun(task string) (string, error) {
	if a.workers == nil {
		return "", errors.New("worker runtime is unavailable")
	}
	return a.workers.StartWithApproval(task, "", a.approvalProvider)
}

func (a *App) StartWorkerRunWithConfig(task, body string) (string, error) {
	if a.workers == nil {
		return "", errors.New("worker runtime is unavailable")
	}
	var config orchestrator.Config
	if err := json.Unmarshal([]byte(body), &config); err != nil {
		return "", err
	}
	return a.workers.StartWithConfigAndApproval(task, config, a.approvalProvider)
}

func (a *App) WorkerRuns() ([]orchestrator.Run, error) {
	if a.workers == nil {
		return nil, errors.New("worker runtime is unavailable")
	}
	return a.workers.List()
}

func (a *App) WorkerRunSnapshot(id string) (orchestrator.Run, error) {
	if a.workers == nil {
		return orchestrator.Run{}, errors.New("worker runtime is unavailable")
	}
	return a.workers.Snapshot(id)
}

func (a *App) CancelWorkerRun(id string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.Cancel(id)
}

func (a *App) ContinueWorkerRun(id, task string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.ContinueWithApproval(id, task, a.approvalProvider)
}

func (a *App) RenameWorkerRun(id, title string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.Rename(id, title)
}

func (a *App) DeleteWorkerRun(id string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.Delete(id)
}

func (a *App) PlanWorkerDAG(task, body string, parallel int) (string, error) {
	if a.workers == nil {
		return "", errors.New("worker runtime is unavailable")
	}
	var config orchestrator.Config
	if err := json.Unmarshal([]byte(body), &config); err != nil {
		return "", err
	}
	return a.workers.PlanDAG(task, config, parallel)
}

func (a *App) SaveWorkerDAG(id, body string, parallel int) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	var tasks []orchestrator.DAGTask
	if err := json.Unmarshal([]byte(body), &tasks); err != nil {
		return err
	}
	return a.workers.UpdateDAG(id, tasks, parallel)
}

func (a *App) ExecuteWorkerDAG(id string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.ExecuteDAG(id, a.approvalProvider)
}

func (a *App) ReviewWorkerDAGTask(id, taskID, decision string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.ReviewDAGTask(id, taskID, decision)
}

func (a *App) MergeWorkerDAG(id string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.MergeDAG(id)
}

func (a *App) ResetWorkerDAGTask(id, taskID string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.ResetDAGTask(id, taskID)
}

func (a *App) CleanupWorkerDAG(id string) error {
	if a.workers == nil {
		return errors.New("worker runtime is unavailable")
	}
	return a.workers.CleanupDAG(id)
}
