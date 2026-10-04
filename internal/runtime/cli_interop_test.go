package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// Exercise the real adapter dispatch/parsers without accounts or model calls.
// Each CLI must receive the same task contract and keep its own model/permissions.
func TestCLIWorkersDispatchAndRetainFailuresAcrossAdapters(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("process fixtures use POSIX shell scripts")
	}
	for _, cli := range []string{"codex", "claude", "openclaude", "opencode", "praimate-code", "copilot"} {
		t.Run(cli, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("PRAIMATE_HOME", root)
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("PWD", root)
			workspace := t.TempDir()
			core.RegisterAllCLIAdapters()
			adapter, err := core.GetCLIAdapter(cli)
			if err != nil {
				t.Fatal(err)
			}
			for _, outcome := range []string{"success", "provider-error", "deadline", "exit-error"} {
				t.Run(outcome, func(t *testing.T) {
					start, activity, terminal := workerCLIFixture(cli)
					script := "#!/bin/sh\ncat > task.txt\nprintf '%s\\n' \"$@\" > args.txt\npwd > workspace.txt\nprintf '%s\\n' '" + start + "' '" + activity + "'\n"
					switch outcome {
					case "success":
						script += "printf '%s\\n' '" + terminal + "'\n"
					case "provider-error":
						failure := `{"type":"session.error","properties":{"error":{"message":"Fixture provider unavailable"}}}`
						if cli == "codex" {
							failure = `{"type":"turn.failed","error":{"message":"Fixture provider unavailable"}}`
						} else if cli == "claude" || cli == "openclaude" {
							failure = `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["Fixture provider unavailable"]}`
						} else if cli == "copilot" {
							failure = `{"type":"session.error","data":{"message":"Fixture provider unavailable"}}`
						}
						script += "printf '%s\\n' '" + failure + "'\n"
					case "deadline":
						script += "exec sleep 30\n"
					case "exit-error":
						script += "printf '%s\\n' '" + terminal + "'\nprintf '%s\\n' 'Fixture process crashed' >&2\nexit 7\n"
					}
					if err := os.WriteFile(filepath.Join(binDir, cli), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
					var progress []ProgressEvent
					req := Request{Task: "Inspect the fixture.\nReturn scoped findings.", SystemPrompt: "Follow the worker protocol.\nReturn one JSON action.", Model: cli + "-selected-model", WorkspaceRoot: workspace,
						Progress: func(e ProgressEvent) { progress = append(progress, e) }}
					if outcome == "deadline" {
						req.Limits.Timeout = 300 * time.Millisecond
					}
					result, err := (CLI{Adapter: adapter}).Execute(context.Background(), req)
					const answer = `{"action":"final","content":"scoped findings"}`
					if result == nil || result.Content != answer {
						t.Fatalf("worker lost output: %+v %v", result, err)
					}
					switch outcome {
					case "success":
						if err != nil || result.Usage.Source != "provider" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 {
							t.Fatalf("worker success/usage contract failed: %+v %v", result, err)
						}
					case "provider-error":
						if err == nil || !strings.Contains(err.Error(), "Fixture provider unavailable") {
							t.Fatalf("failed provider reported as success: %v", err)
						}
					case "deadline":
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("deadline was lost: %v", err)
						}
					case "exit-error":
						if err == nil || !strings.Contains(err.Error(), "Fixture process crashed") {
							t.Fatalf("process exit/stderr was lost: %v", err)
						}
					}
					task, taskErr := os.ReadFile(filepath.Join(workspace, "task.txt"))
					args, argsErr := os.ReadFile(filepath.Join(workspace, "args.txt"))
					cwd, cwdErr := os.ReadFile(filepath.Join(workspace, "workspace.txt"))
					if taskErr != nil || argsErr != nil || cwdErr != nil || !strings.Contains(string(task), req.Task) || strings.TrimSpace(string(cwd)) != workspace || !strings.Contains(string(args), req.Model) {
						t.Fatalf("wrong task/model/workspace: task=%q args=%q cwd=%q errors=%v/%v/%v", task, args, cwd, taskErr, argsErr, cwdErr)
					}
					if !strings.Contains(string(task), req.SystemPrompt) && !strings.Contains(string(args), "--append-system-prompt\n"+req.SystemPrompt+"\n") {
						t.Fatal("worker protocol was not delivered")
					}
					if strings.Contains(string(args), "--allow-all") || strings.Contains(string(args), "--dangerously-skip-permissions") {
						t.Fatalf("worker permission mode changed: %s", args)
					}
					if (cli == "opencode" || cli == "praimate-code") && !strings.Contains(string(args), "--agent\nplan\n") {
						t.Fatal("OpenCode worker did not use the plan agent")
					}
					var started, reasoning, toolStart, toolEnd, failure bool
					for _, e := range progress {
						started = started || e.Kind == "backend_status" && strings.Contains(e.Text, "process started")
						reasoning = reasoning || e.Kind == "reasoning" && e.Text == "Checking scope."
						toolStart = toolStart || e.Kind == "tool_start"
						toolEnd = toolEnd || e.Kind == "tool_end"
						failure = failure || e.Kind == "error" && strings.Contains(e.Text, "Fixture provider unavailable")
					}
					if !started || !reasoning || !toolStart || !toolEnd || outcome == "provider-error" && !failure {
						t.Fatalf("worker live activity lost: %+v", progress)
					}
				})
			}
		})
	}
}

func workerCLIFixture(cli string) (start, activity, terminal string) {
	if cli == "codex" {
		return `{"type":"thread.started","thread_id":"s"}`, strings.Join([]string{
			`{"type":"item.completed","item":{"type":"reasoning","text":"Checking scope."}}`,
			`{"type":"item.started","item":{"type":"command_execution","command":"inspect fixture","id":"tool"}}`,
			`{"type":"item.completed","item":{"type":"command_execution","command":"inspect fixture","id":"tool","status":"completed","exit_code":0}}`,
			`{"type":"item.completed","item":{"type":"agent_message","text":"{\"action\":\"final\",\"content\":\"scoped findings\"}"}}`,
		}, "\n"), `{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":3}}`
	}
	if cli == "claude" || cli == "openclaude" {
		return `{"type":"system","subtype":"init","session_id":"s"}`, strings.Join([]string{
			`{"type":"assistant","message":{"id":"m","content":[{"type":"thinking","thinking":"Checking scope."},{"type":"tool_use","id":"tool","name":"Read","input":{"file_path":"fixture"}},{"type":"text","text":"{\"action\":\"final\",\"content\":\"scoped findings\"}"}],"usage":{"input_tokens":12,"output_tokens":3}}}`,
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool","is_error":false}]}}`,
		}, "\n"), `{"type":"result","result":"{\"action\":\"final\",\"content\":\"scoped findings\"}","usage":{"input_tokens":12,"output_tokens":3}}`
	}
	if cli == "copilot" {
		return `{"type":"session.start","data":{"sessionId":"s"}}`, strings.Join([]string{
			`{"type":"assistant.reasoning","data":{"reasoningId":"r","content":"Checking scope."}}`,
			`{"type":"tool.execution_start","data":{"toolCallId":"tool","toolName":"view"}}`,
			`{"type":"tool.execution_complete","data":{"toolCallId":"tool","success":true}}`,
			`{"type":"assistant.message","data":{"content":"{\"action\":\"final\",\"content\":\"scoped findings\"}"}}`,
			`{"type":"assistant.usage","id":"usage","data":{"inputTokens":12,"outputTokens":3}}`,
		}, "\n"), `{"type":"result","sessionId":"s","exitCode":0}`
	}
	return `{"type":"step_start","sessionID":"s","part":{"id":"step"}}`, strings.Join([]string{
		`{"type":"reasoning","part":{"text":"Checking scope."}}`,
		`{"type":"tool_use","part":{"id":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"fixture"},"output":"ok"}}}`,
		`{"type":"text","part":{"text":"{\"action\":\"final\",\"content\":\"scoped findings\"}"}}`,
	}, "\n"), `{"type":"step_finish","part":{"id":"step","tokens":{"input":12,"output":3}}}`
}
