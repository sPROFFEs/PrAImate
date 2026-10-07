package orchestrator

import "time"

func recordWorkerActivity(run *Run, event Event) {
	recordAttempt(run, event)
	if len(event.Text) > 64<<10 {
		event.Text = truncateWorkerText(event.Text, 64<<10)
	}
	last := len(run.Events) - 1
	merge := (event.Kind == "stream" || event.Kind == "reasoning") && last >= 0 && len(run.pendingEvents) > 0 &&
		run.Events[last].Kind == event.Kind && run.Events[last].WorkerID == event.WorkerID &&
		run.Events[last].TaskID == event.TaskID && run.Events[last].Tier == event.Tier &&
		run.pendingEvents[len(run.pendingEvents)-1].Sequence == run.Events[last].Sequence &&
		len(run.Events[last].Text)+len(event.Text) <= 64<<10
	if merge {
		run.Events[last].Text += event.Text
		run.Events[last].Timestamp = event.Timestamp
		run.pendingEvents[len(run.pendingEvents)-1] = run.Events[last]
	} else {
		run.EventSequence++
		event.Sequence = run.EventSequence
		run.Events = append(run.Events, event)
		run.pendingEvents = append(run.pendingEvents, event)
	}
	if len(run.Events) > 500 {
		run.Events = append([]Event(nil), run.Events[len(run.Events)-500:]...)
	}
	bytes := 0
	for i := len(run.Events) - 1; i >= 0; i-- {
		bytes += len(run.Events[i].Text)
		if bytes > 1<<20 {
			run.Events = append([]Event(nil), run.Events[i+1:]...)
			break
		}
	}
	run.UpdatedAt = time.Now().UTC()
}
