package main

import "errors"

type workerWindowRequest struct {
	ID       string `json:"id"`
	Task     string `json:"task,omitempty"`
	TaskID   string `json:"taskID,omitempty"`
	Decision string `json:"decision,omitempty"`
	Body     string `json:"body,omitempty"`
	Parallel int    `json:"parallel,omitempty"`
}

func (d *detachedCoordinator) callWorker(w *detachedWindow, req detachedRPCRequest) (any, error) {
	if w.kind != "workers" {
		return nil, errors.New("operation is outside this window's scope")
	}
	var body workerWindowRequest
	if err := decodeRPCBody(req.Body, &body); err != nil {
		return nil, err
	}
	if body.ID != w.sessionID {
		return nil, errors.New("worker run is outside this detached window")
	}
	a := d.app
	switch req.Method {
	case "worker.approvals":
		return a.WorkerRunApprovals(body.ID), nil
	case "worker.snapshot":
		return a.WorkerRunSnapshot(body.ID)
	case "worker.cancel":
		return nil, a.CancelWorkerRun(body.ID)
	case "worker.continue":
		return nil, a.ContinueWorkerRun(body.ID, body.Task)
	case "worker.config.update":
		return nil, a.UpdateWorkerRunConfig(body.ID, body.Body)
	case "worker.plan.retry":
		return nil, a.RetryWorkerPlanning(body.ID)
	case "worker.graph.save":
		return nil, a.SaveWorkerDAG(body.ID, body.Body, body.Parallel)
	case "worker.graph.execute":
		return nil, a.ExecuteWorkerDAG(body.ID)
	case "worker.graph.review":
		return nil, a.ReviewWorkerDAGTask(body.ID, body.TaskID, body.Decision)
	case "worker.graph.merge":
		return nil, a.MergeWorkerDAG(body.ID)
	case "worker.graph.reset":
		return nil, a.ResetWorkerDAGTask(body.ID, body.TaskID)
	case "worker.graph.profile":
		return nil, a.UseWorkerDAGTaskProfile(body.ID, body.TaskID)
	case "worker.graph.cleanup":
		return nil, a.CleanupWorkerDAG(body.ID)
	default:
		return nil, errors.New("unsupported worker window operation")
	}
}

func (a *App) WorkerRunApprovals(id string) []ApprovalRequest {
	if a.detachedClient != nil {
		var requests []ApprovalRequest
		_ = a.detachedClient.rpc("worker.approvals", workerWindowRequest{ID: id}, &requests)
		return requests
	}
	a.approvalMu.Lock()
	b := a.approval
	a.approvalMu.Unlock()
	requests := []ApprovalRequest{}
	if b != nil {
		b.mu.Lock()
		for _, request := range b.requests {
			if request.ChatID == id {
				requests = append(requests, request)
			}
		}
		b.mu.Unlock()
	}
	return requests
}
