package orchestrator

import "time"

func recordWorkerActivity(run *Run, event Event) {
	if len(event.Text) > 64<<10 {
		event.Text = truncateWorkerText(event.Text, 64<<10)
	}
	last := len(run.Events) - 1
	if (event.Kind == "stream" || event.Kind == "reasoning") && last >= 0 &&
		run.Events[last].Kind == event.Kind && run.Events[last].WorkerID == event.WorkerID &&
		run.Events[last].TaskID == event.TaskID && run.Events[last].Tier == event.Tier &&
		len(run.Events[last].Text)+len(event.Text) <= 64<<10 {
		run.Events[last].Text += event.Text
		run.Events[last].Timestamp = event.Timestamp
	} else {
		run.Events = append(run.Events, event)
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
