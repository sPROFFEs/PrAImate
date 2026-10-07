package orchestrator

func recordAttempt(run *Run, event Event) {
	if event.WorkerID == "" {
		return
	}
	index := -1
	for i := range run.Attempts {
		if run.Attempts[i].ID == event.WorkerID {
			index = i
			break
		}
	}
	if index < 0 {
		run.Attempts = append(run.Attempts, Attempt{ID: event.WorkerID, TaskID: event.TaskID, ParentID: event.ParentID, Tier: event.Tier, Runtime: event.Runtime, CLI: event.CLI, Model: event.Model, ReasoningEffort: event.ReasoningEffort, Workspace: event.Workspace, Status: "running", StartedAt: event.Timestamp, TimeoutSeconds: event.TimeoutSeconds})
		index = len(run.Attempts) - 1
	}
	a := &run.Attempts[index]
	if event.ProfileHash != "" {
		a.ProfileHash = event.ProfileHash
	}
	a.UpdatedAt = event.Timestamp
	if event.Phase != "" {
		a.Phase = event.Phase
	}
	if event.Step != 0 {
		a.Step = event.Step
	}
	if event.SessionID != "" {
		a.SessionID = event.SessionID
	}
	switch event.Kind {
	case "started":
		a.Assignment = event.Text
	case "request", "input":
		a.Status = "running"
		a.CallStartedAt = event.Timestamp
	case "delegation":
		a.Status = "waiting"
	case "delegated_result", "delegated_error":
		a.Status = "running"
	case "stream", "reasoning", "tool_start", "tool_end", "backend_status":
		a.LastProgressAt = event.Timestamp
	case "response", "output":
		a.Output = truncateWorkerText(event.Text, 4096)
		if event.Usage.Source == "provider" {
			a.Usage.Input += event.Usage.InputTokens
			a.Usage.Output += event.Usage.OutputTokens
			a.Usage.Calls++
			run.Usage.Input += event.Usage.InputTokens
			run.Usage.Output += event.Usage.OutputTokens
			run.Usage.Calls++
		}
	case "result":
		a.Output = truncateWorkerText(event.Text, 4096)
	case "completed", "failed", "cancelled":
		a.Status = event.Kind
		if event.Kind != "completed" {
			a.Error = event.Text
		}
	}
}

func restoreAttempts(run *Run) {
	if len(run.Attempts) == 0 {
		for _, event := range run.Events {
			recordAttempt(run, event)
		}
	}
	for i := range run.Attempts {
		a := &run.Attempts[i]
		if a.Status == "running" || a.Status == "waiting" {
			a.Status = "interrupted"
			a.Error = "Execution interrupted. Inspect changes before continuing."
		}
	}
}
