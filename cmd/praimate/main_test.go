package main

import "testing"

func TestSupportedOS_GUIOnlyMatrix(t *testing.T) {
	for _, tc := range []struct {
		goos string
		want bool
	}{
		{"linux", true},
		{"windows", true},
		{"darwin", true},
		{"freebsd", false},
	} {
		if got := supportedOS(tc.goos); got != tc.want {
			t.Errorf("supportedOS(%q) = %v, want %v", tc.goos, got, tc.want)
		}
	}
}

func TestRun_DefaultAndGUIAliasLaunchDesktop(t *testing.T) {
	old := launchDesktop
	t.Cleanup(func() { launchDesktop = old })

	calls := 0
	launchDesktop = func() int {
		calls++
		return 23
	}
	if got := run(nil); got != 23 {
		t.Fatalf("default run returned %d, want desktop result", got)
	}
	if got := run([]string{"--gui"}); got != 23 {
		t.Fatalf("--gui run returned %d, want desktop result", got)
	}
	if calls != 2 {
		t.Fatalf("desktop launches = %d, want 2", calls)
	}
}

func TestRunAgentCommandWiresWorkflowInputsAndRunControls(t *testing.T) {
	old := executeAgentPrompt
	t.Cleanup(func() { executeAgentPrompt = old })
	var got agentPromptOptions
	executeAgentPrompt = func(opts agentPromptOptions) int {
		got = opts
		return 17
	}
	code := run([]string{
		"agent", "run", "--agent", "docx-worker", "--workflow", "Rearrange",
		"--input", "source=/in/a.docx", "--input", "output=/out/a.docx",
		"--folder", ".", "--run-id", "job-42", "--retry",
	})
	if code != 17 {
		t.Fatalf("exit=%d", code)
	}
	if got.AgentID != "docx-worker" || got.Workflow != "Rearrange" || got.RunID != "job-42" || !got.Retry {
		t.Fatalf("options=%#v", got)
	}
	if len(got.Inputs) != 2 || got.Inputs[0] != "source=/in/a.docx" || got.Inputs[1] != "output=/out/a.docx" {
		t.Fatalf("inputs=%v", got.Inputs)
	}
}

func TestRunAgentCommandRequiresAgent(t *testing.T) {
	if code := run([]string{"agent", "run", "--folder", ".", "--prompt", "x"}); code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
}

func TestRunAgentCommandRejectsLegacyInputsOnModernCommand(t *testing.T) {
	if code := run([]string{
		"agent", "run", "--agent", "docx-worker", "--workflow", "Rearrange",
		"--inputs", "source=/in/a.docx",
	}); code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
}
