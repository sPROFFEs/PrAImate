package main

import (
	"context"
	"errors"
	"strings"
)

// Each workflow view owns one cancellation scope and correlation ID.
func (a *App) beginWorkflowRun(id string) (context.Context, func(), error) {
	if strings.TrimSpace(id) == "" || len(id) > 128 {
		return nil, nil, errors.New("a workflow execution ID is required")
	}
	a.workflowCancelMu.Lock()
	defer a.workflowCancelMu.Unlock()
	if a.workflowCancels == nil {
		a.workflowCancels = map[string]context.CancelFunc{}
	}
	if a.workflowCancels[id] != nil {
		return nil, nil, errors.New("this workflow execution is already running")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.workflowCancels[id] = cancel
	return ctx, func() {
		a.workflowCancelMu.Lock()
		delete(a.workflowCancels, id)
		a.workflowCancelMu.Unlock()
		cancel()
	}, nil
}

func (a *App) CancelWorkflowRun(id string) {
	a.workflowCancelMu.Lock()
	cancel := a.workflowCancels[id]
	a.workflowCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) RunWorkflowTracked(runID, agentID, workflowName, cli, model, cwd string, inputs map[string]string, localEndpoint, localModel, tools string) (*RunResult, error) {
	ctx, finish, err := a.beginWorkflowRun(runID)
	if err != nil {
		return nil, err
	}
	defer finish()
	return a.runWorkflow(ctx, runID, agentID, workflowName, cli, model, cwd, inputs, localEndpoint, localModel, tools)
}

func (a *App) RunAllWorkflowsTracked(runID, agentID, cli, model, cwd string, inputs map[string]map[string]string, localEndpoint, localModel, tools string) (*RunResult, error) {
	ctx, finish, err := a.beginWorkflowRun(runID)
	if err != nil {
		return nil, err
	}
	defer finish()
	return a.runAllWorkflows(ctx, runID, agentID, cli, model, cwd, inputs, localEndpoint, localModel, tools)
}
