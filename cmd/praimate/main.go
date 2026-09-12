// praimate is the GUI bootstrap and maintenance CLI.
//
// Running it without an action launches the sibling praimate-gui
// desktop application. The remaining flags are non-interactive
// maintenance and automation entrypoints.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/installer"
	"git.jtsec.local/lab/PrAImate/internal/updater"
	"git.jtsec.local/lab/PrAImate/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

var (
	launchDesktop      = launchGUI
	executeAgentPrompt = runAgentPrompt
	executeAgentStatus = runAgentStatus
	executeModelCheck  = runModelCheck
)

func run(args []string) int {
	if len(args) >= 1 && args[0] == "code" {
		return runCode(args[1:])
	}
	// Long forms for automation. The top-level --agent spelling remains as a
	// compact alias for scripts and CI jobs.
	agentCommand, modelCommand := "", ""
	if len(args) >= 2 && args[0] == "agent" && (args[1] == "run" || args[1] == "status") {
		agentCommand, args = args[1], args[2:]
	} else if len(args) >= 2 && args[0] == "model" && args[1] == "check" {
		modelCommand, args = args[1], args[2:]
	}

	flags := flag.NewFlagSet("praimate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	versionFlag := flags.Bool("version", false, "print version and exit")
	flags.BoolVar(versionFlag, "v", false, "print version and exit")
	guiFlag := flags.Bool("gui", false, "launch the desktop app (default; retained for compatibility)")
	checkUpdateFlag := flags.Bool("check-update", false, "check GitHub for a newer release and exit")
	updateFlag := flags.Bool("update", false, "download and install the latest release, then exit")
	yesFlag := flags.Bool("y", false, "auto-confirm the update prompt")
	installTool := flags.String("install-tool", "",
		"install a PrAImate-managed tool and exit (graphify, gstack, scrapegraph, praimate-code)")
	mergeMemory := flags.Bool("merge-memory", false,
		"git merge driver hook for per-chat MEMORY.md files; not for direct use")
	runAgent := flags.String("run-agent", "",
		"run an agent workflow non-interactively and print the assistant reply")
	runCLI := flags.String("cli", "", "CLI used for non-interactive agent execution (default: agent's first supported CLI)")
	runWorkflow := flags.String("workflow", "", "workflow used with -run-agent")
	runInputs := flags.String("inputs", "", "comma-separated key=value inputs for -run-agent")
	var agentInputs stringListFlag
	flags.Var(&agentInputs, "input", "workflow input as key=value; repeat for multiple values")
	agentPrompt := flags.String("agent", "", "run one agent prompt without opening the GUI")
	agentFolder := flags.String("folder", "", "project folder for --agent")
	agentPromptText := flags.String("prompt", "", "prompt for --agent (prefer --prompt-file for sensitive or large prompts)")
	agentPromptFile := flags.String("prompt-file", "", "read the --agent prompt from a file; use - for stdin")
	agentModel := flags.String("model", "", "optional model override for --agent")
	agentEndpoint := flags.String("endpoint", "", "use 'saved' or the Local LLM endpoint configured in the GUI (requires --model)")
	agentTools := flags.String("tools", "safe", "headless tool policy: safe, edits, or full")
	agentOutput := flags.String("output", "json", "headless output: json, jsonl, or text")
	agentTimeout := flags.Duration("timeout", 30*time.Minute, "maximum --agent execution time; 0 disables the deadline")
	agentPersist := flags.Bool("persist", false, "keep the headless run in Chats (default: remove its temporary chat)")
	agentRunID := flags.String("run-id", "", "caller-supplied durable idempotency key and status ID")
	agentRetry := flags.Bool("retry", false, "explicitly retry a failed/interrupted durable --run-id")
	skipModelPreflight := flags.Bool("skip-model-preflight", false, "skip authenticated model-list validation for a saved endpoint")
	modelProbe := flags.Bool("probe", false, "with model check, run a tiny generation probe through --cli")
	dbPasswordStdin := flags.Bool("db-password-stdin", false, "read the database password securely from the terminal, or from piped stdin")
	listAgents := flags.Bool("list-agents", false, "print every agent in the database and exit")
	importTemplate := flags.String("import-template", "", "import legacy workpath template(s) and exit")

	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, version.Banner)
		fmt.Fprintf(os.Stderr, "\nUsage: praimate [command] [options]\n\n")
		fmt.Fprintln(os.Stderr, "Commands:")
		fmt.Fprintln(os.Stderr, "  (none)                     Launch desktop application (default)")
		fmt.Fprintln(os.Stderr, "  code [args...]             Launch managed PrAImate Code CLI")
		fmt.Fprintln(os.Stderr, "  agent run [options]        Execute an agent headless/non-interactively")
		fmt.Fprintln(os.Stderr, "  agent status [options]     Check durable agent run status")
		fmt.Fprintln(os.Stderr, "  model check [options]      Check model connectivity and permissions")
		fmt.Fprintln(os.Stderr, "\nGeneral Options:")
		fmt.Fprintln(os.Stderr, "  --version, -v              Print version and exit")
		fmt.Fprintln(os.Stderr, "  --help, -h                 Print this help and exit")
		fmt.Fprintln(os.Stderr, "  --check-update             Check for newer release on GitHub/mirror")
		fmt.Fprintln(os.Stderr, "  --update                   Download and apply latest update")
		fmt.Fprintln(os.Stderr, "  --install-tool <name>      Install tool (graphify, gstack, scrapegraph, praimate-code)")
		fmt.Fprintln(os.Stderr, "  --list-agents              List all agents stored in database")
		fmt.Fprintln(os.Stderr, "\nAgent Execution Options (`praimate agent run`):")
		fmt.Fprintln(os.Stderr, "  --agent <id>               Target agent ID (required)")
		fmt.Fprintln(os.Stderr, "  --cli <name>               CLI to use (claude, openclaude, codex, opencode, praimate-code)")
		fmt.Fprintln(os.Stderr, "  --folder <path>            Working directory for agent execution")
		fmt.Fprintln(os.Stderr, "  --prompt <text>            Single-turn prompt to execute")
		fmt.Fprintln(os.Stderr, "  --prompt-file <path>       Read prompt from file or stdin ('-')")
		fmt.Fprintln(os.Stderr, "  --workflow <name>          Run a specific agent workflow")
		fmt.Fprintln(os.Stderr, "  --input <k=v>              Workflow inputs (repeatable: --input key=val)")
		fmt.Fprintln(os.Stderr, "  --model <name>             Model override")
		fmt.Fprintln(os.Stderr, "  --endpoint <url>           Custom/saved Local LLM endpoint")
		fmt.Fprintln(os.Stderr, "  --tools <safe|edits|full>  Tool permissions policy (default: safe)")
		fmt.Fprintln(os.Stderr, "  --output <json|jsonl|text> Output format (default: json)")
		fmt.Fprintln(os.Stderr, "  --timeout <duration>       Execution deadline (e.g. 15m, default: 30m)")
		fmt.Fprintln(os.Stderr, "  --persist                  Keep session in Chats database")
		fmt.Fprintln(os.Stderr, "  --run-id <id>              Durable idempotency/status run ID")
		fmt.Fprintln(os.Stderr, "  --db-password-stdin        Prompt for or read database password securely")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *versionFlag {
		fmt.Println(version.Banner)
		fmt.Printf("\n%s %s %s/%s\n", version.Name, version.Current, runtime.GOOS, runtime.GOARCH)
		return 0
	}
	if !supportedOS(runtime.GOOS) {
		fmt.Fprintf(os.Stderr, "praimate: unsupported operating system %q; PrAImate supports Linux and Windows only\n", runtime.GOOS)
		return 1
	}
	if *mergeMemory {
		return runMergeMemory(flags.Args())
	}
	if *checkUpdateFlag {
		return runCheckUpdate()
	}
	if *updateFlag {
		return runUpdate(*yesFlag)
	}
	if *installTool != "" {
		return runInstallTool(*installTool)
	}
	if *listAgents {
		return runListAgents()
	}
	if *importTemplate != "" {
		return runImportTemplate(*importTemplate)
	}
	if agentCommand == "status" {
		return executeAgentStatus(agentStatusOptions{RunID: *agentRunID, Output: *agentOutput, DBPasswordStdin: *dbPasswordStdin})
	}
	if modelCommand == "check" {
		return executeModelCheck(modelCheckOptions{
			CLI: *runCLI, Folder: *agentFolder, Model: *agentModel, Endpoint: *agentEndpoint,
			Output: *agentOutput, Timeout: *agentTimeout, Probe: *modelProbe,
			DBPasswordStdin: *dbPasswordStdin,
		})
	}
	if *runAgent != "" {
		if len(agentInputs) > 0 {
			fmt.Fprintln(os.Stderr, "praimate: -run-agent uses legacy --inputs; use `agent run --input key=value` for the modern API")
			return 2
		}
		cli := *runCLI
		if cli == "" {
			cli = "claude" // preserve the legacy -run-agent default
		}
		return runAgentWorkflow(*runAgent, cli, *runWorkflow, *runInputs)
	}
	if *agentPrompt != "" {
		if strings.TrimSpace(*runInputs) != "" {
			fmt.Fprintln(os.Stderr, "praimate: --inputs is legacy; use repeatable --input key=value with agent run")
			return 2
		}
		return executeAgentPrompt(agentPromptOptions{
			AgentID: *agentPrompt, CLI: *runCLI, Folder: *agentFolder,
			Prompt: *agentPromptText, PromptFile: *agentPromptFile,
			Workflow: *runWorkflow, Inputs: agentInputs,
			Model: *agentModel, Endpoint: *agentEndpoint, SkipModelPreflight: *skipModelPreflight,
			Tools: *agentTools, Output: *agentOutput,
			Timeout: *agentTimeout, Persist: *agentPersist, RunID: *agentRunID, Retry: *agentRetry,
			DBPasswordStdin: *dbPasswordStdin,
		})
	}
	if agentCommand == "run" {
		fmt.Fprintln(os.Stderr, "praimate: agent run requires --agent")
		return 2
	}

	_ = guiFlag // `--gui` is now an explicit spelling of the default.
	return launchDesktop()
}

func supportedOS(goos string) bool {
	return goos == "linux" || goos == "windows"
}

// runMergeMemory is wired into workspace repositories as the
// `clade-memory` merge driver. It preserves the per-chat MEMORY.md
// behavior; it is unrelated to the removed cross-chat database memory.
func runMergeMemory(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "praimate --merge-memory needs 3 paths (ancestor, ours, theirs)")
		return 2
	}
	ours, theirs := args[1], args[2]
	a, err := os.ReadFile(ours)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read ours: %v\n", err)
		return 1
	}
	b, err := os.ReadFile(theirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read theirs: %v\n", err)
		return 1
	}
	combined := string(a)
	if !strings.HasSuffix(combined, "\n") {
		combined += "\n"
	}
	combined += "\n## --- merged from another machine at " +
		time.Now().UTC().Format(time.RFC3339) + " ---\n\n"
	combined += string(b)
	if !strings.HasSuffix(combined, "\n") {
		combined += "\n"
	}
	if err := os.WriteFile(ours, []byte(combined), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write ours: %v\n", err)
		return 1
	}
	return 0
}

func runCheckUpdate() int {
	rel, err := updater.FetchLatest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "praimate: %v\n", err)
		return 1
	}
	if updater.IsNewer(rel.TagName, version.Current) {
		fmt.Printf("update available: %s (currently %s)\n  %s\n", rel.TagName, version.Current, rel.HTMLURL)
		fmt.Println("\nRun `praimate -update` to install it.")
		return 0
	}
	fmt.Printf("up to date (%s is the latest release)\n", version.Current)
	return 0
}

func runUpdate(autoYes bool) int {
	rel, err := updater.FetchLatest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "praimate: %v\n", err)
		return 1
	}
	if !updater.IsNewer(rel.TagName, version.Current) {
		fmt.Printf("already on the latest release (%s)\n", version.Current)
		return 0
	}
	asset, err := updater.AssetForHost(rel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "praimate: %v\n", err)
		return 1
	}

	fmt.Printf("Update available: %s → %s\n", version.Current, rel.TagName)
	fmt.Printf("Asset: %s (%.1f MB)\n", asset.Name, float64(asset.Size)/(1024*1024))
	fmt.Printf("Release notes: %s\n\n", rel.HTMLURL)
	if !autoYes {
		fmt.Print("Install now? [y/N] ")
		var answer string
		_, _ = fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" && answer != "yes" {
			fmt.Println("cancelled")
			return 1
		}
	}
	if err := updater.Apply(asset, func(stage string) {
		fmt.Println("  …", stage)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "praimate: update failed: %v\n", err)
		return 1
	}
	fmt.Printf("\n✓ installed %s. Re-run `praimate` to start the new version.\n", rel.TagName)
	return 0
}

func runInstallTool(name string) int {
	if name == "praimate-code" {
		if err := installer.InstallPraimateCode(context.Background(), os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "\npraimate: install praimate-code failed: %v\n", err)
			if errors.Is(err, installer.ErrNoPrebuiltAsset) {
				fmt.Fprintln(os.Stderr, "       build it from source instead: scripts/build-praimate-code.sh (needs git + bun)")
			}
			return 1
		}
		fmt.Println("\n✓ praimate-code installed. Launch it with: praimate code")
		return 0
	}

	id := installer.ToolID(name)
	known := false
	var hint string
	for _, tool := range installer.KnownTools() {
		hint += " " + string(tool.ID)
		if tool.ID == id {
			known = true
		}
	}
	if !known {
		fmt.Fprintf(os.Stderr, "praimate: unknown tool %q. Available:%s praimate-code\n", name, hint)
		return 2
	}
	methods := installer.ToolMethods(id, installer.ActionInstall, installer.DetectOS())
	if len(methods) == 0 {
		fmt.Fprintf(os.Stderr, "praimate: no install method available for %s on this OS\n", name)
		fmt.Fprintln(os.Stderr, "       common missing prerequisites: uv for graphify/scrapegraph; git+bun+bash for gstack")
		fmt.Fprintln(os.Stderr, "       Linux:  curl -LsSf https://astral.sh/uv/install.sh | sh")
		fmt.Fprintln(os.Stderr, "       Windows: irm https://astral.sh/uv/install.ps1 | iex")
		return 1
	}
	method := methods[0]
	fmt.Printf("Installing %s\n  method: %s\n  command: %s\n\n", name, method.Label, method.Command)
	if err := installer.Run(context.Background(), method, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "\npraimate: install %s failed: %v\n", name, err)
		return 1
	}
	fmt.Printf("\n✓ %s installed. New chats and sandboxes will pick it up automatically.\n", name)
	return 0
}
