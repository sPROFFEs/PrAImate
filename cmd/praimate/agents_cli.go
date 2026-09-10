// Non-interactive agent utility handlers used by the maintenance CLI.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
	"git.jtsec.local/lab/PrAImate/internal/store"
	"golang.org/x/term"
)

// openCore opens the default PrAImate DB, builds a Core, seeds the
// built-in agents, and registers production CLI adapters. Returns the
// Core and a cleanup function the caller must defer.
func openCore() (*core.Core, func(), error) {
	return openCoreWithPassword("")
}

// openCoreWithPassword opens secure storage with either the explicit
// password supplied by a headless caller or the remembered OS credential.
// Passwords are deliberately never accepted as command-line values because
// argv is visible to other local processes on common Linux configurations.
func openCoreWithPassword(password string) (*core.Core, func(), error) {
	dbPath, err := store.DefaultDBPath()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve db path: %w", err)
	}
	var st *store.Store
	if password != "" {
		st, err = store.OpenWithPassword(dbPath, password)
	} else {
		st, err = store.Open(dbPath)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	if _, err := c.SeedBuiltins(context.Background()); err != nil {
		st.Close()
		return nil, nil, fmt.Errorf("seed builtins: %w", err)
	}
	core.RegisterAllCLIAdapters()
	return c, func() { _ = st.Close() }, nil
}

const agentRunSchema = "praimate.agent-run/v1"

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

var (
	agentRunInput                io.Reader = os.Stdin
	agentRunOutput               io.Writer = os.Stdout
	agentRunError                io.Writer = os.Stderr
	openAgentRunCore                       = openCoreWithPassword
	readAgentRunTerminalPassword           = readTerminalPassword
	agentRunTerminalAvailable              = agentRunInputIsTerminal
)

type agentPromptOptions struct {
	AgentID            string
	CLI                string
	Folder             string
	Prompt             string
	PromptFile         string
	Workflow           string
	Inputs             []string
	Model              string
	Endpoint           string
	SkipModelPreflight bool
	Tools              string
	Output             string
	Timeout            time.Duration
	Persist            bool
	RunID              string
	Retry              bool
	Durable            bool
	DBPasswordStdin    bool
}

type agentRunResult struct {
	Schema       string `json:"schema"`
	OK           bool   `json:"ok"`
	AgentID      string `json:"agentId"`
	AgentName    string `json:"agentName,omitempty"`
	CLI          string `json:"cli,omitempty"`
	Runtime      string `json:"runtime,omitempty"`
	RunID        string `json:"runId,omitempty"`
	ManagedRunID string `json:"managedRunId,omitempty"`
	State        string `json:"state,omitempty"`
	Attempt      int    `json:"attempt,omitempty"`
	Cached       bool   `json:"cached,omitempty"`
	Workflow     string `json:"workflow,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
	Turns        int    `json:"turns,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	Reply        string `json:"reply"`
	DurationMs   int64  `json:"durationMs"`
	ExitCode     int    `json:"exitCode,omitempty"`
	Error        string `json:"error,omitempty"`
}

type agentRunEvent struct {
	Schema    string `json:"schema"`
	Type      string `json:"type"`
	AgentID   string `json:"agentId"`
	RunID     string `json:"runId,omitempty"`
	Workflow  string `json:"workflow,omitempty"`
	Turn      *int   `json:"turn,omitempty"`
	EventType string `json:"eventType,omitempty"`
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Detail    string `json:"detail,omitempty"`
	ID        string `json:"id,omitempty"`
	OK        *bool  `json:"ok,omitempty"`
}

func runAgentPrompt(opts agentPromptOptions) int {
	opts.Output = strings.ToLower(strings.TrimSpace(opts.Output))
	if opts.Output == "" {
		opts.Output = "json"
	}
	if opts.Output != "json" && opts.Output != "jsonl" && opts.Output != "text" {
		return writeAgentFailure(opts, 2, "--output must be json, jsonl, or text")
	}
	opts.RunID = strings.TrimSpace(opts.RunID)
	opts.Durable = opts.RunID != ""
	if opts.Retry && !opts.Durable {
		return writeAgentFailure(opts, 2, "--retry requires a caller-supplied --run-id")
	}
	if opts.RunID == "" {
		var err error
		opts.RunID, err = newExternalRunID()
		if err != nil {
			return writeAgentFailure(opts, 1, err.Error())
		}
	}

	workflow := strings.TrimSpace(opts.Workflow)
	if workflow != "" && (opts.Prompt != "" || opts.PromptFile != "") {
		return writeAgentFailure(opts, 2, "--workflow cannot be combined with --prompt or --prompt-file")
	}
	inputs, err := parseStrictInputs(opts.Inputs)
	if err != nil {
		return writeAgentFailure(opts, 2, err.Error())
	}
	if workflow == "" && len(inputs) > 0 {
		return writeAgentFailure(opts, 2, "--input requires --workflow")
	}
	prompt := ""
	if workflow == "" {
		promptUsesStdin := opts.PromptFile == "-" || (opts.Prompt == "" && opts.PromptFile == "")
		if opts.DBPasswordStdin && promptUsesStdin {
			return writeAgentFailure(opts, 2, "stdin cannot carry both the prompt and database password; use --prompt or --prompt-file")
		}
		prompt, err = readAgentPrompt(opts)
		if err != nil {
			return writeAgentFailure(opts, 2, err.Error())
		}
	}
	password := strings.TrimSpace(os.Getenv("PRAIMATE_DB_PASSWORD"))
	_ = os.Unsetenv("PRAIMATE_DB_PASSWORD")
	if opts.DBPasswordStdin {
		if agentRunTerminalAvailable() {
			password, err = readAgentRunTerminalPassword()
		} else {
			password, err = readPasswordLine(agentRunInput)
		}
		if err != nil {
			return writeAgentFailure(opts, 2, err.Error())
		}
	}

	folder, err := resolveAgentFolder(opts.Folder)
	if err != nil {
		return writeAgentFailure(opts, 2, err.Error())
	}
	tools := strings.ToLower(strings.TrimSpace(opts.Tools))
	endpoint, err := resolveAgentEndpoint(opts.Endpoint)
	if err != nil {
		return writeAgentFailure(opts, 2, err.Error())
	}
	model := strings.TrimSpace(opts.Model)
	if endpoint != "" && model == "" {
		return writeAgentFailure(opts, 2, "--endpoint requires --model to select the model served by that endpoint")
	}
	if tools == "safe" {
		tools = ""
	}
	if tools != "" && tools != "edits" && tools != "full" {
		return writeAgentFailure(opts, 2, "--tools must be safe, edits, or full")
	}
	c, cleanup, err := openAgentRunCore(password)
	if err != nil && password == "" && errors.Is(err, store.ErrPasswordRequired) && agentRunTerminalAvailable() {
		password, err = readAgentRunTerminalPassword()
		if err == nil {
			c, cleanup, err = openAgentRunCore(password)
		}
	}
	password = ""
	if err != nil {
		if errors.Is(err, store.ErrPasswordRequired) {
			err = errors.New("database password required: enable Remember password, set PRAIMATE_DB_PASSWORD, or use --db-password-stdin")
		}
		return writeAgentFailure(opts, 1, err.Error())
	}
	defer cleanup()

	ctx := context.Background()
	if opts.Timeout < 0 {
		return writeAgentFailure(opts, 2, "--timeout cannot be negative")
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	agent, err := c.GetAgent(ctx, strings.TrimSpace(opts.AgentID))
	if err != nil {
		return writeAgentFailure(opts, 1, err.Error())
	}
	cli := strings.TrimSpace(opts.CLI)
	if cli == "" {
		if len(agent.Supports) == 0 {
			return writeAgentFailure(opts, 1, fmt.Sprintf("agent %q supports no CLI", agent.ID))
		}
		cli = agent.Supports[0]
	}
	if endpoint != "" {
		if err := core.ValidateLocalRoutingCLI(cli); err != nil {
			return writeAgentFailure(opts, 1, err.Error())
		}
	}

	// Managed tools fail closed unless the caller explicitly selected an
	// automation policy. "edits" approves only project writes; "full"
	// approves every capability declared by the agent runtime manifest.
	if tools != "" {
		c.SetApprovalProvider(func(string) *core.ApprovalConfig {
			return &core.ApprovalConfig{Request: func(_ context.Context, tool string, _ map[string]any) (bool, error) {
				return tools == "full" || (tools == "edits" && tool == "project.write"), nil
			}}
		})
	}

	effective, err := c.ResolveEffectiveAgentConfig(ctx, agent)
	if err != nil {
		return writeAgentFailure(opts, 1, err.Error())
	}
	kind := "prompt"
	if workflow != "" {
		kind = "workflow"
	}
	requestHash, err := externalRunRequestHash(
		agent.ID, cli, folder, kind, prompt, workflow, inputs, model, endpoint, tools,
		opts.Timeout, opts.Persist, opts.SkipModelPreflight,
	)
	if err != nil {
		return writeAgentFailure(opts, 1, err.Error())
	}
	var claimed *core.ExternalAgentRun
	if opts.Durable {
		var execute bool
		var claimErr error
		claimed, execute, claimErr = c.ClaimExternalAgentRun(ctx, core.ClaimExternalAgentRunRequest{
			ID: opts.RunID, RequestHash: requestHash, AgentID: agent.ID, CLI: cli,
			Runtime: string(effective.Mode), Kind: kind, Retry: opts.Retry,
		})
		if claimErr != nil {
			return writeAgentFailure(opts, 1, claimErr.Error())
		}
		if !execute {
			return replayExternalAgentRun(opts, claimed)
		}
	}
	attempt := 1
	if claimed != nil {
		attempt = claimed.Attempt
	}
	if endpoint != "" && !opts.SkipModelPreflight {
		if err := requireLocalModel(ctx, c, endpoint, model); err != nil {
			return finishAndWriteAgentFailure(c, opts, agent, cli, string(effective.Mode), attempt, 1, "failed", "model preflight: "+err.Error())
		}
	}

	started := time.Now()
	ctx = core.WithSkillTaskBudget(ctx, "external:"+opts.RunID)
	if workflow != "" {
		var onEvent func(core.WorkflowRunEvent)
		if opts.Output == "jsonl" {
			enc := json.NewEncoder(agentRunOutput)
			onEvent = func(ev core.WorkflowRunEvent) {
				turn := ev.TurnIndex
				_ = enc.Encode(agentRunEvent{
					Schema: agentRunSchema, Type: "event", AgentID: agent.ID, RunID: opts.RunID,
					Workflow: ev.WorkflowName, Turn: &turn, EventType: ev.Type,
					Text: ev.Text, Tool: ev.Tool, Detail: ev.Detail, ID: ev.ID, OK: agentEventOK(ev.Type, ev.OK),
				})
			}
		}
		settings := core.ChatSettings{Tools: tools, ToolsConfigured: true}
		if endpoint != "" {
			settings.Local = &core.ChatLocalEndpoint{Endpoint: endpoint, Model: model}
		}
		res := c.RunWorkflow(ctx, core.RunOptions{
			Agent: agent, WorkflowName: workflow, Inputs: inputs, CLI: cli, Cwd: folder,
			Model: model, Tools: tools, OnEvent: onEvent, Persist: opts.Persist,
			ChatSettings: settings,
		})
		duration := time.Since(started).Milliseconds()
		result := agentRunResult{
			Schema: agentRunSchema, AgentID: agent.ID, AgentName: agent.Name, CLI: cli,
			Runtime: string(effective.Mode), RunID: opts.RunID, ManagedRunID: res.RunID,
			State: "completed", Attempt: attempt, Workflow: res.WorkflowName,
			Outcome: string(res.Outcome), Turns: len(res.Turns), DurationMs: duration,
		}
		if len(res.Turns) > 0 && res.Turns[len(res.Turns)-1].Reply != nil {
			result.Reply = res.Turns[len(res.Turns)-1].Reply.Text
		}
		if opts.Persist {
			result.ChatID = res.ChatID
		}
		if res.Err != nil {
			code, state := 1, "failed"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code, state = 124, "timed_out"
			}
			result.State, result.ExitCode, result.Error = state, code, res.Err.Error()
			return finishAndWriteAgentResult(c, opts, result, code)
		}
		result.OK = true
		return finishAndWriteAgentResult(c, opts, result, 0)
	}

	chat, err := c.StartInteractiveChat(ctx, agent.ID, cli, folder)
	if err != nil {
		return finishAndWriteAgentFailure(c, opts, agent, cli, string(effective.Mode), attempt, 1, "failed", err.Error())
	}
	if !opts.Persist {
		defer func() { _ = c.DeleteChat(context.Background(), chat.ID) }()
	}
	// Always persist the policy, including explicit Safe (empty). Otherwise an
	// agent runtime's default_tools could silently widen a headless run.
	if err := c.UpdateChatConfig(ctx, chat.ID, cli, model, tools); err != nil {
		return finishAndWriteAgentFailure(c, opts, agent, cli, string(effective.Mode), attempt, 1, "failed", err.Error())
	}
	if endpoint != "" {
		if err := c.UpdateChatSettings(ctx, chat.ID, func(s *core.ChatSettings) {
			s.Model = ""
			s.Local = &core.ChatLocalEndpoint{Endpoint: endpoint, Model: model}
		}); err != nil {
			return finishAndWriteAgentFailure(c, opts, agent, cli, string(effective.Mode), attempt, 1, "failed", err.Error())
		}
	}

	var onEvent core.StreamHandler
	if opts.Output == "jsonl" {
		enc := json.NewEncoder(agentRunOutput)
		onEvent = func(ev core.StreamEvent) {
			_ = enc.Encode(agentRunEvent{
				Schema: agentRunSchema, Type: "event", AgentID: agent.ID, RunID: opts.RunID,
				EventType: ev.Type, Text: ev.Text, Tool: ev.Tool,
				Detail: ev.Detail, ID: ev.ID, OK: agentEventOK(ev.Type, ev.OK),
			})
		}
	}
	turn, err := c.ContinueChatStream(ctx, chat.ID, prompt, folder, core.AgentSystemPrompt(agent), nil, onEvent)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		code, state := 1, "failed"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code, state = 124, "timed_out"
		}
		result := agentRunResult{
			Schema: agentRunSchema, AgentID: agent.ID, AgentName: agent.Name, CLI: cli,
			Runtime: string(effective.Mode), RunID: opts.RunID, State: state, Attempt: attempt,
			DurationMs: duration, ExitCode: code, Error: err.Error(),
		}
		if opts.Persist {
			result.ChatID = chat.ID
		}
		return finishAndWriteAgentResult(c, opts, result, code)
	}
	result := agentRunResult{
		Schema: agentRunSchema, OK: true, AgentID: agent.ID, AgentName: agent.Name,
		CLI: cli, Runtime: string(effective.Mode), RunID: opts.RunID,
		State: "completed", Attempt: attempt, ManagedRunID: turn.ManagedRunID,
		Reply: turn.Reply, DurationMs: duration,
	}
	if opts.Persist {
		result.ChatID = chat.ID
	}
	return finishAndWriteAgentResult(c, opts, result, 0)
}

func agentEventOK(eventType string, ok bool) *bool {
	switch eventType {
	case "tool_end", "step_finish", "workflow_finish", "turn_finish", "error":
		value := ok
		return &value
	default:
		return nil
	}
}

func newExternalRunID() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return fmt.Sprintf("run-%s-%x", time.Now().UTC().Format("20060102T150405"), suffix[:]), nil
}

func parseStrictInputs(values []string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, value := range values {
		key, raw, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --input %q; expected key=value", value)
		}
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("duplicate --input key %q", key)
		}
		out[key] = strings.TrimSpace(raw)
	}
	return out, nil
}

func externalRunRequestHash(agentID, cli, folder, kind, prompt, workflow string, inputs map[string]string, model, endpoint, tools string, timeout time.Duration, persist, skipModelPreflight bool) (string, error) {
	body := struct {
		AgentID            string            `json:"agentId"`
		CLI                string            `json:"cli"`
		Folder             string            `json:"folder"`
		Kind               string            `json:"kind"`
		Prompt             string            `json:"prompt,omitempty"`
		Workflow           string            `json:"workflow,omitempty"`
		Inputs             map[string]string `json:"inputs,omitempty"`
		Model              string            `json:"model,omitempty"`
		Endpoint           string            `json:"endpoint,omitempty"`
		Tools              string            `json:"tools,omitempty"`
		TimeoutNanoseconds int64             `json:"timeoutNanoseconds"`
		Persist            bool              `json:"persist"`
		SkipModelPreflight bool              `json:"skipModelPreflight"`
	}{
		agentID, cli, folder, kind, prompt, workflow, inputs, model,
		ollama.NormalizeEndpoint(endpoint), tools, int64(timeout), persist, skipModelPreflight,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("hash run request: %w", err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}

func localLLMAPIKey(ctx context.Context, c *core.Core) (string, error) {
	raw, err := c.GetSetting(ctx, core.ScopeCLI, "local_llm.api_key")
	if err != nil || len(raw) == 0 {
		return "", err
	}
	var key string
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", fmt.Errorf("decode local LLM API key: %w", err)
	}
	return strings.TrimSpace(key), nil
}

func requireLocalModel(ctx context.Context, c *core.Core, endpoint, model string) error {
	key, err := localLLMAPIKey(ctx, c)
	if err != nil {
		return err
	}
	models, err := ollama.ListModels(ctx, ollama.NormalizeEndpoint(endpoint), key)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	for _, candidate := range models {
		if candidate == model {
			return nil
		}
	}
	return fmt.Errorf("model %q is not listed by the saved endpoint", model)
}

func replayExternalAgentRun(opts agentPromptOptions, run *core.ExternalAgentRun) int {
	if run == nil {
		return writeAgentFailure(opts, 1, "durable run state is unavailable")
	}
	if run.ResultJSON == "" {
		return writeAgentFailure(opts, 1, fmt.Sprintf("run %q is %s; use --retry only after confirming no other process is executing it", run.ID, run.State))
	}
	var result agentRunResult
	if err := json.Unmarshal([]byte(run.ResultJSON), &result); err != nil {
		return writeAgentFailure(opts, 1, "decode cached run result: "+err.Error())
	}
	result.Cached = true
	result.Attempt = run.Attempt
	_ = writeAgentResult(opts.Output, result)
	if result.ExitCode != 0 {
		return result.ExitCode
	}
	if !result.OK {
		return 1
	}
	return 0
}

func finishAndWriteAgentFailure(c *core.Core, opts agentPromptOptions, agent *core.Agent, cli, runtime string, attempt, code int, state, message string) int {
	result := agentRunResult{
		Schema: agentRunSchema, RunID: opts.RunID, AgentID: opts.AgentID, CLI: cli,
		Runtime: runtime, State: state, Attempt: attempt, ExitCode: code, Error: message,
	}
	if agent != nil {
		result.AgentID, result.AgentName = agent.ID, agent.Name
	}
	return finishAndWriteAgentResult(c, opts, result, code)
}

func finishAndWriteAgentResult(c *core.Core, opts agentPromptOptions, result agentRunResult, code int) int {
	if opts.Durable && c != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			result.OK, result.State, result.ExitCode, result.Error = false, "failed", 1, "encode durable result: "+err.Error()
			code = 1
		} else if err := c.FinishExternalAgentRun(context.Background(), opts.RunID, result.Attempt, result.State, string(raw), result.Error); err != nil {
			result.OK, result.State, result.ExitCode, result.Error = false, "failed", 1, "persist durable result: "+err.Error()
			code = 1
		}
	}
	_ = writeAgentResult(opts.Output, result)
	return code
}

type agentStatusOptions struct {
	RunID           string
	Output          string
	DBPasswordStdin bool
}

type agentStatusResult struct {
	Schema      string          `json:"schema"`
	OK          bool            `json:"ok"`
	RunID       string          `json:"runId"`
	State       string          `json:"state,omitempty"`
	AgentID     string          `json:"agentId,omitempty"`
	CLI         string          `json:"cli,omitempty"`
	Runtime     string          `json:"runtime,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Attempt     int             `json:"attempt,omitempty"`
	CreatedAt   string          `json:"createdAt,omitempty"`
	UpdatedAt   string          `json:"updatedAt,omitempty"`
	CompletedAt string          `json:"completedAt,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

func runAgentStatus(opts agentStatusOptions) int {
	opts.Output = strings.ToLower(strings.TrimSpace(opts.Output))
	if opts.Output == "" {
		opts.Output = "json"
	}
	if opts.Output != "json" && opts.Output != "text" {
		return writeStatusResult("json", agentStatusResult{Schema: "praimate.agent-run-status/v1", Error: "--output must be json or text"}, 2)
	}
	opts.RunID = strings.TrimSpace(opts.RunID)
	if opts.RunID == "" {
		return writeStatusResult(opts.Output, agentStatusResult{Schema: "praimate.agent-run-status/v1", Error: "agent status requires --run-id"}, 2)
	}
	c, cleanup, err := openAgentUtilityCore(opts.DBPasswordStdin)
	if err != nil {
		return writeStatusResult(opts.Output, agentStatusResult{Schema: "praimate.agent-run-status/v1", RunID: opts.RunID, Error: err.Error()}, 1)
	}
	defer cleanup()
	run, err := c.GetExternalAgentRun(context.Background(), opts.RunID)
	if err != nil {
		return writeStatusResult(opts.Output, agentStatusResult{Schema: "praimate.agent-run-status/v1", RunID: opts.RunID, Error: err.Error()}, 1)
	}
	result := agentStatusResult{
		Schema: "praimate.agent-run-status/v1", OK: true, RunID: run.ID, State: run.State,
		AgentID: run.AgentID, CLI: run.CLI, Runtime: run.Runtime, Kind: run.Kind, Attempt: run.Attempt,
		CreatedAt: run.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: run.UpdatedAt.Format(time.RFC3339Nano),
	}
	if run.CompletedAt != nil {
		result.CompletedAt = run.CompletedAt.Format(time.RFC3339Nano)
	}
	if run.ResultJSON != "" {
		result.Result = json.RawMessage(run.ResultJSON)
	}
	result.Error = run.Error
	return writeStatusResult(opts.Output, result, 0)
}

func writeStatusResult(output string, result agentStatusResult, code int) int {
	if output == "text" {
		if result.Error != "" && !result.OK {
			fmt.Fprintln(agentRunError, "praimate:", result.Error)
		} else {
			fmt.Fprintf(agentRunOutput, "%s %s attempt=%d\n", result.RunID, result.State, result.Attempt)
		}
		return code
	}
	_ = json.NewEncoder(agentRunOutput).Encode(result)
	return code
}

type modelCheckOptions struct {
	CLI             string
	Folder          string
	Model           string
	Endpoint        string
	Output          string
	Timeout         time.Duration
	Probe           bool
	DBPasswordStdin bool
}

type modelCheckResult struct {
	Schema     string   `json:"schema"`
	OK         bool     `json:"ok"`
	Endpoint   string   `json:"endpoint,omitempty"`
	Model      string   `json:"model,omitempty"`
	Present    bool     `json:"present"`
	Responding bool     `json:"responding,omitempty"`
	CLI        string   `json:"cli,omitempty"`
	Models     []string `json:"models,omitempty"`
	DurationMs int64    `json:"durationMs"`
	Error      string   `json:"error,omitempty"`
}

func runModelCheck(opts modelCheckOptions) int {
	opts.Output = strings.ToLower(strings.TrimSpace(opts.Output))
	if opts.Output == "" {
		opts.Output = "json"
	}
	started := time.Now()
	result := modelCheckResult{Schema: "praimate.model-check/v1", Model: strings.TrimSpace(opts.Model)}
	write := func(code int, err error) int {
		result.DurationMs = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = err.Error()
		} else {
			result.OK = true
		}
		if opts.Output == "text" {
			if err != nil {
				fmt.Fprintln(agentRunError, "praimate:", err)
			} else {
				fmt.Fprintf(agentRunOutput, "%s present=%t responding=%t\n", result.Model, result.Present, result.Responding)
			}
		} else {
			_ = json.NewEncoder(agentRunOutput).Encode(result)
		}
		return code
	}
	if opts.Output != "json" && opts.Output != "text" {
		return write(2, errors.New("--output must be json or text"))
	}
	if result.Model == "" {
		return write(2, errors.New("model check requires --model"))
	}
	endpoint, err := resolveAgentEndpoint(opts.Endpoint)
	if err != nil {
		return write(2, err)
	}
	if endpoint == "" {
		return write(2, errors.New("model check requires --endpoint saved"))
	}
	result.Endpoint = endpoint
	c, cleanup, err := openAgentUtilityCore(opts.DBPasswordStdin)
	if err != nil {
		return write(1, err)
	}
	defer cleanup()
	ctx := context.Background()
	if opts.Timeout < 0 {
		return write(2, errors.New("--timeout cannot be negative"))
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	key, err := localLLMAPIKey(ctx, c)
	if err != nil {
		return write(1, err)
	}
	result.Models, err = ollama.ListModels(ctx, ollama.NormalizeEndpoint(endpoint), key)
	if err != nil {
		return write(1, fmt.Errorf("list models: %w", err))
	}
	for _, candidate := range result.Models {
		result.Present = result.Present || candidate == result.Model
	}
	if !result.Present {
		return write(1, fmt.Errorf("model %q is not listed by the saved endpoint", result.Model))
	}
	if opts.Probe {
		folder := strings.TrimSpace(opts.Folder)
		if folder == "" {
			folder, err = os.Getwd()
		} else {
			folder, err = resolveAgentFolder(folder)
		}
		if err != nil {
			return write(2, err)
		}
		cli := strings.TrimSpace(opts.CLI)
		if cli == "" {
			cli = "praimate-code"
		}
		result.CLI = cli
		adapter, err := core.GetCLIAdapter(cli)
		if err != nil {
			return write(1, err)
		}
		if err := adapter.Available(ctx); err != nil {
			return write(1, err)
		}
		cfg, err := c.ResolveExecutionConfig(ctx, core.ExecutionRequest{
			Surface: core.SurfaceWorkflow, CLI: cli, Cwd: folder, ToolsConfigured: true,
			Local: &core.ChatLocalEndpoint{Endpoint: endpoint, Model: result.Model},
		})
		if err != nil {
			return write(1, err)
		}
		if err := c.PrepareExecution(ctx, cfg); err != nil {
			return write(1, err)
		}
		reply, err := adapter.SingleShot(ctx, core.SingleShotOpts{
			Cwd: folder, Message: "Reply exactly PRAIMATE_MODEL_READY", Model: cfg.Model, Tools: "", Env: cfg.Env,
		})
		if err != nil {
			return write(1, err)
		}
		if reply.ExitCode != 0 {
			return write(1, fmt.Errorf("%s exited with code %d: %s", cli, reply.ExitCode, reply.Text))
		}
		result.Responding = strings.TrimSpace(reply.Text) != ""
		if !result.Responding {
			return write(1, errors.New("model probe returned an empty response"))
		}
	}
	return write(0, nil)
}

func openAgentUtilityCore(dbPasswordStdin bool) (*core.Core, func(), error) {
	password := strings.TrimSpace(os.Getenv("PRAIMATE_DB_PASSWORD"))
	_ = os.Unsetenv("PRAIMATE_DB_PASSWORD")
	var err error
	if dbPasswordStdin {
		if agentRunTerminalAvailable() {
			password, err = readAgentRunTerminalPassword()
		} else {
			password, err = readPasswordLine(agentRunInput)
		}
		if err != nil {
			return nil, nil, err
		}
	}
	c, cleanup, err := openAgentRunCore(password)
	if err != nil && password == "" && errors.Is(err, store.ErrPasswordRequired) && agentRunTerminalAvailable() {
		password, err = readAgentRunTerminalPassword()
		if err == nil {
			c, cleanup, err = openAgentRunCore(password)
		}
	}
	password = ""
	if errors.Is(err, store.ErrPasswordRequired) {
		err = errors.New("database password required: enable Remember password, set PRAIMATE_DB_PASSWORD, or use --db-password-stdin")
	}
	return c, cleanup, err
}

func readAgentPrompt(opts agentPromptOptions) (string, error) {
	if opts.Prompt != "" && opts.PromptFile != "" {
		return "", errors.New("use only one of --prompt or --prompt-file")
	}
	var raw []byte
	var err error
	switch {
	case opts.PromptFile == "-":
		raw, err = io.ReadAll(agentRunInput)
	case opts.PromptFile != "":
		raw, err = os.ReadFile(opts.PromptFile)
	case opts.Prompt != "":
		raw = []byte(opts.Prompt)
	default:
		file, ok := agentRunInput.(*os.File)
		if ok {
			info, statErr := file.Stat()
			if statErr != nil || info.Mode()&os.ModeCharDevice != 0 {
				return "", errors.New("a prompt is required via --prompt, --prompt-file, or piped stdin")
			}
		}
		raw, err = io.ReadAll(agentRunInput)
	}
	if err != nil {
		return "", fmt.Errorf("read prompt: %w", err)
	}
	prompt := strings.TrimSpace(string(raw))
	if prompt == "" {
		return "", errors.New("prompt is empty")
	}
	return prompt, nil
}

func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, 64<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read database password: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("database password is empty")
	}
	return line, nil
}

func agentRunInputIsTerminal() bool {
	f, ok := agentRunInput.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// readTerminalPassword reads directly from the controlling stdin terminal
// with echo disabled. The prompt goes to stderr so stdout stays valid JSON.
func readTerminalPassword() (string, error) {
	f, ok := agentRunInput.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", store.ErrPasswordRequired
	}
	fmt.Fprint(agentRunError, "PrAImate database password: ")
	raw, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(agentRunError)
	if err != nil {
		return "", fmt.Errorf("read database password: %w", err)
	}
	password := strings.TrimSpace(string(raw))
	for i := range raw {
		raw[i] = 0
	}
	if password == "" {
		return "", errors.New("database password is empty")
	}
	return password, nil
}

func resolveAgentFolder(folder string) (string, error) {
	if strings.TrimSpace(folder) == "" {
		return "", errors.New("--folder is required")
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", fmt.Errorf("resolve --folder: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("--folder is not an accessible directory: %s", abs)
	}
	return abs, nil
}

// resolveAgentEndpoint only permits the endpoint already selected in the GUI.
// Otherwise a headless caller could redirect the encrypted API key to an
// attacker-controlled URL. "saved" keeps automation portable across machines.
func resolveAgentEndpoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return "", fmt.Errorf("load saved local endpoint: %w", err)
	}
	if cfg == nil || strings.TrimSpace(cfg.DefaultLocalEndpoint) == "" {
		return "", errors.New("--endpoint requires a saved endpoint; configure one in the Local LLM tab first")
	}
	saved := strings.TrimSpace(cfg.DefaultLocalEndpoint)
	if strings.EqualFold(value, "saved") {
		return saved, nil
	}
	if ollama.NormalizeEndpoint(value) != ollama.NormalizeEndpoint(saved) {
		return "", errors.New("--endpoint must be 'saved' or match the endpoint configured in the Local LLM tab")
	}
	return saved, nil
}

func writeAgentFailure(opts agentPromptOptions, code int, message string) int {
	result := agentRunResult{
		Schema: agentRunSchema, AgentID: opts.AgentID, CLI: opts.CLI,
		RunID: opts.RunID, State: "failed", ExitCode: code, Error: message,
	}
	output := opts.Output
	if output == "" {
		output = "json"
	}
	if output == "text" {
		fmt.Fprintln(agentRunError, "praimate:", message)
		return code
	}
	_ = writeAgentResult(output, result)
	return code
}

func writeAgentResult(output string, result agentRunResult) int {
	if output == "text" {
		fmt.Fprintln(agentRunOutput, result.Reply)
		return 0
	}
	if output == "jsonl" {
		line := struct {
			Type string `json:"type"`
			agentRunResult
		}{Type: "result", agentRunResult: result}
		_ = json.NewEncoder(agentRunOutput).Encode(line)
		return 0
	}
	_ = json.NewEncoder(agentRunOutput).Encode(result)
	return 0
}

// runListAgents implements `praimate -list-agents`.
func runListAgents() int {
	c, cleanup, err := openCore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "praimate:", err)
		return 1
	}
	defer cleanup()

	agents, err := c.ListAgents(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "praimate:", err)
		return 1
	}
	if len(agents) == 0 {
		fmt.Println("(no agents — built-ins should auto-seed; this is a bug)")
		return 0
	}
	fmt.Printf("%-22s %-12s %s\n", "ID", "SUPPORTS", "DESCRIPTION")
	for _, a := range agents {
		supports := strings.Join(a.Supports, ",")
		desc := strings.SplitN(a.Description, "\n", 2)[0]
		fmt.Printf("%-22s %-12s %s\n", a.ID, supports, desc)
	}
	return 0
}

// runAgentWorkflow implements `praimate -run-agent <id> [-cli ...] [-workflow ...] [-inputs k=v,...]`.
//
// Inputs use the same comma-separated key=value format as go test -run;
// quoted values with commas are not supported (this is a developer-
// facing utility, not the primary UX). Use the GUI for richer input
// collection.
func runAgentWorkflow(agentID, cli, workflow, inputsRaw string) int {
	c, cleanup, err := openCore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "praimate:", err)
		return 1
	}
	defer cleanup()

	ctx := context.Background()
	agent, err := c.GetAgent(ctx, agentID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "praimate:", err)
		return 1
	}

	inputs := parseInputsCSV(inputsRaw)
	cwd, _ := os.Getwd()

	start := time.Now()
	res := c.RunWorkflow(ctx, core.RunOptions{
		Agent:        agent,
		WorkflowName: workflow,
		Inputs:       inputs,
		CLI:          cli,
		Cwd:          cwd,
		OnTurn: func(t core.TurnResult) {
			fmt.Printf("--- turn %d (%dms) ---\n", t.Index+1, t.DurationMs)
			fmt.Println(t.Reply.Text)
			fmt.Println()
		},
	})
	elapsed := time.Since(start)

	if res.Err != nil {
		fmt.Fprintf(os.Stderr, "praimate: workflow %s/%s failed (%s after %s): %v\n",
			res.AgentID, res.WorkflowName, res.Outcome, elapsed, res.Err)
		return 1
	}
	fmt.Printf("=== %s (%d turns in %s) ===\n", res.Outcome, len(res.Turns), elapsed)
	return 0
}

// parseInputsCSV parses "k=v,k2=v2" into a map. Empty input → empty map.
// Whitespace around keys/values is trimmed. Duplicate keys: last wins.
// Pairs without "=" are skipped silently — the runner's own validation
// will reject missing required inputs with a useful error.
func parseInputsCSV(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	for _, pair := range strings.Split(s, ",") {
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(pair[:eq])
		v := strings.TrimSpace(pair[eq+1:])
		out[k] = v
	}
	return out
}

// runImportTemplate implements `praimate -import-template <dir>`. It
// converts a pre-1.1 workpath template into an agent with its knowledge
// base. Pass a single dir, or a parent dir to import every template
// subdirectory inside it (skips _common and any dir without a template
// marker).
func runImportTemplate(path string) int {
	c, cleanup, err := openCore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "praimate:", err)
		return 1
	}
	defer cleanup()
	ctx := context.Background()

	var dirs []string
	if core.IsWorkpathTemplate(path) {
		dirs = []string{path}
	} else {
		entries, rerr := os.ReadDir(path)
		if rerr != nil {
			fmt.Fprintln(os.Stderr, "praimate:", rerr)
			return 1
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == "_common" || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			sub := filepath.Join(path, e.Name())
			if core.IsWorkpathTemplate(sub) {
				dirs = append(dirs, sub)
			}
		}
	}
	if len(dirs) == 0 {
		fmt.Fprintf(os.Stderr, "praimate: no workpath templates found under %s\n", path)
		return 1
	}

	rc := 0
	for _, d := range dirs {
		agent, err := c.ImportWorkpathTemplate(ctx, d, "", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", filepath.Base(d), err)
			rc = 1
			continue
		}
		files, _ := core.ListAgentKnowledge(agent.ID)
		fmt.Printf("  ✓ %s → agent %q (%d knowledge files)\n", filepath.Base(d), agent.ID, len(files))
	}
	return rc
}
