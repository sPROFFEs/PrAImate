// wpc is the workpath compiler: it reads a tool-agnostic workpath directory
// and emits per-CLI-agent artifacts (Claude Code skills, mika workpaths,
// Cursor rules, generic AGENTS.md).
//
// Usage:
//
//	wpc validate <source-dir>
//	wpc compile  <source-dir> --target <name|all> --out <dir>
//	wpc init     <name> [--in <dir>]
//	wpc targets
//	wpc help
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/pkg/targets"
	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "validate":
		err = cmdValidate(args)
	case "compile":
		err = cmdCompile(args)
	case "init":
		err = cmdInit(args)
	case "targets":
		err = cmdTargets(args)
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "wpc: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "wpc: %v\n", err)
		os.Exit(1)
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `wpc — workpath compiler

Usage:
  wpc validate <source-dir>
  wpc compile  <source-dir> --target <name|all> --out <dir>
  wpc init     <name> [--in <dir>]
  wpc targets
  wpc help

Source format:
  <name>/
    workpath.json       (optional: description, version, tool/agent overrides)
    mission.md          (required: what this workpath is for)
    playbook.md         (optional: staged process)
    rules.md            (optional: hard constraints)
    tools/*.sh|*.ps1    (optional: scripts, auto-registered)
    agents/*.md         (optional: subagent prompts)

Examples:
  wpc validate samples/workpaths/reversing
  wpc compile  samples/workpaths/reversing --target claude --out build/
  wpc compile  samples/workpaths/reversing --target all    --out build/
`)
}

func cmdValidate(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("validate: expected <source-dir>")
	}
	wp, err := workpath.LoadDir(args[0])
	if err != nil {
		return err
	}
	if err := workpath.Validate(wp); err != nil {
		return err
	}
	fmt.Printf("ok: %s (%d tools, %d agents)\n", wp.Name, len(wp.Tools), len(wp.Agents))
	return nil
}

func cmdCompile(args []string) error {
	fs := flag.NewFlagSet("compile", flag.ContinueOnError)
	target := fs.String("target", "", "target name or 'all'")
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("compile: expected <source-dir>")
	}
	if *target == "" {
		return fmt.Errorf("compile: --target is required (one of: %s, or 'all')", strings.Join(targets.Names(), ", "))
	}
	if *out == "" {
		return fmt.Errorf("compile: --out is required")
	}
	wp, err := workpath.LoadDir(rest[0])
	if err != nil {
		return err
	}
	if err := workpath.Validate(wp); err != nil {
		return err
	}
	absOut, err := filepath.Abs(*out)
	if err != nil {
		return err
	}

	var picked []targets.Target
	if *target == "all" {
		picked = targets.All()
	} else {
		t, err := targets.Get(*target)
		if err != nil {
			return err
		}
		picked = []targets.Target{t}
	}

	for _, t := range picked {
		if err := t.Compile(wp, absOut); err != nil {
			return fmt.Errorf("%s: %w", t.Name(), err)
		}
		fmt.Printf("  %s → %s\n", t.Name(), absOut)
	}
	return nil
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	in := fs.String("in", ".", "parent directory to create the workpath inside")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fmt.Errorf("init: expected <name>")
	}
	name := rest[0]
	root := filepath.Join(*in, name)
	if _, err := os.Stat(root); err == nil {
		return fmt.Errorf("init: %s already exists", root)
	}
	files := map[string]string{
		"workpath.json": fmt.Sprintf("{\n  \"description\": \"One-line summary of the %s workpath\",\n  \"version\": \"1\"\n}\n", name),
		"mission.md":    fmt.Sprintf("# %s\n\nDescribe the mission this workpath exists to accomplish — the desired outcome, the inputs the agent will receive, and the artifacts it should produce.\n", name),
		"playbook.md":   "## Stage 1 — Scout\n\n- (describe step)\n\n## Stage 2 — Execute\n\n- (describe step)\n",
		"rules.md":      "- Never (hard constraint)\n- Always (hard constraint)\n",
	}
	for rel, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			return err
		}
	}
	for _, sub := range []string{"tools", "agents"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return err
		}
		gk := filepath.Join(root, sub, ".gitkeep")
		if err := os.WriteFile(gk, []byte{}, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("created %s\n", root)
	return nil
}

// reorderFlags moves --flag (and --flag=val / --flag val) arguments before
// positional ones, so Go's stdlib flag package — which stops at the first
// positional — accepts either order on the command line.
//
// Heuristic: any arg starting with "-" is a flag. If it doesn't contain "=",
// the next arg is treated as its value (unless that next arg is itself a
// flag). Conservative; works for our four known flags.
func reorderFlags(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

func cmdTargets(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("targets: takes no arguments")
	}
	for _, t := range targets.All() {
		fmt.Printf("  %-8s  %s\n", t.Name(), t.Description())
	}
	return nil
}
