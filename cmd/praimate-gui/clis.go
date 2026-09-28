package main

// CLIs tab bindings — detection + installation of the wrapped CLI
// agents: list the known CLIs with
// live detection status, surface the per-OS install methods from
// internal/installer, and run the chosen method with its output
// streamed to the frontend over "praimate:install" events.

import (
	"context"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sPROFFEs/PrAImate/internal/installer"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

// CLIBackend is one wrapped CLI with its detection status.
type CLIBackend struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Binary      string `json:"binary"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	ProbeError  string `json:"probeError,omitempty"`
	InstallHint string `json:"installHint,omitempty"`
}

// ListCLIBackends detects every known CLI (8s probe each, parallel via
// launcher.DetectAgents) and returns the status list for the CLIs tab.
func (a *App) ListCLIBackends() ([]CLIBackend, error) {
	refreshManagedPaths()
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	detected := launcher.DetectAgents(ctx)
	out := make([]CLIBackend, 0, len(detected))
	for _, ag := range detected {
		out = append(out, CLIBackend{
			ID:          string(ag.ID),
			Label:       ag.Label,
			Binary:      ag.Binary,
			Installed:   ag.Available,
			Version:     ag.Version,
			ProbeError:  ag.ProbeError,
			InstallHint: ag.InstallHint,
		})
	}
	return out, nil
}

// InstallMethod is one way to install/update a CLI on this OS.
type InstallMethod struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Command        string   `json:"command"`
	Recommended    bool     `json:"recommended"`
	MissingPrereqs []string `json:"missingPrereqs,omitempty"`
}

// ListInstallMethods returns the runnable install methods for a CLI on
// the current OS, recommended first (installer.Methods ordering).
func (a *App) ListInstallMethods(cli string) ([]InstallMethod, error) {
	refreshManagedPaths()
	methods := installer.Methods(installer.AgentID(cli), installer.ActionInstall, installer.DetectOS())
	out := make([]InstallMethod, 0, len(methods))
	for _, m := range methods {
		out = append(out, InstallMethod{
			ID:             m.ID,
			Label:          m.Label,
			Command:        m.Command,
			Recommended:    m.Recommended,
			MissingPrereqs: installer.BlockingPrereqs(m, installer.RunOptions{InstallNode: true}),
		})
	}
	return out, nil
}

// installLogWriter streams installer output lines to the frontend.
type installLogWriter struct {
	ctx context.Context
	cli string
}

func (w installLogWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		wruntime.EventsEmit(w.ctx, "praimate:install", map[string]string{"cli": w.cli, "line": line})
	}
	return len(p), nil
}

// InstallCLI runs the chosen install method, streaming output over
// "praimate:install". Returns when the method finishes; the frontend
// re-probes the CLI list afterwards.
func (a *App) InstallCLI(cli, methodID string) error {
	refreshManagedPaths()
	methods := installer.Methods(installer.AgentID(cli), installer.ActionInstall, installer.DetectOS())
	for _, m := range methods {
		if m.ID == methodID {
			w := installLogWriter{ctx: a.ctx, cli: cli}
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
			defer cancel()
			err := installer.RunWithOptions(ctx, m, installer.RunOptions{InstallNode: true}, w, w)
			refreshManagedPaths()
			return err
		}
	}
	return errUnknownMethod(cli, methodID)
}

// refreshManagedPaths re-imports the managed install dirs into PATH so
// a tool/CLI installed seconds ago resolves without restarting the app
// (and is inherited by every CLI child the GUI spawns from now on).
// Covers both the PrAImate-managed prefixes AND the user-level dirs the
// just-finished installer might have written to (pnpm/bun/cargo/…).
var pathRefreshMu sync.Mutex

func refreshManagedPaths() {
	pathRefreshMu.Lock()
	defer pathRefreshMu.Unlock()

	installer.ImportPnpmPathIfPresent()
	installer.ImportManagedAgentsToPath()
	installer.ImportManagedToolsToPath()
	installer.ImportPraimateBinToPath()
	installer.ImportUserBinDirs()
	// Windows: pick up PATH dirs the just-finished installer wrote to
	// the registry (scoop shims, npm prefix, bun, …). No-op elsewhere.
	installer.ImportWindowsRegistryPath()
}

// RefreshPATH is the user-triggered version of refreshManagedPaths,
// exposed to the frontend so a "Re-scan" button on the CLIs tab can
// pick up tools the user installed in another terminal without
// restarting PrAImate.
func (a *App) RefreshPATH() {
	refreshManagedPaths()
}

func errUnknownMethod(cli, id string) error {
	return &installError{cli: cli, id: id}
}

type installError struct{ cli, id string }

func (e *installError) Error() string {
	return "no install method " + e.id + " for " + e.cli + " on this OS"
}

// --- managed tools (graphify, gstack, scrapegraph) ----------------------------

// ManagedTool mirrors installer.Tool for the CLIs tab's tools section.
// praimate-code is excluded — it has its own dedicated row.
type ManagedTool struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Binary      string `json:"binary"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version,omitempty"`
	InstallHint string `json:"installHint,omitempty"`
}

// ListManagedTools detects the PrAImate-managed helper tools.
func (a *App) ListManagedTools() ([]ManagedTool, error) {
	refreshManagedPaths()
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	tools := installer.DetectTools(ctx)
	out := make([]ManagedTool, 0, len(tools))
	for _, t := range tools {
		if t.ID == installer.ToolPraimateCode {
			continue
		}
		out = append(out, ManagedTool{
			ID: string(t.ID), Label: t.Label, Binary: t.Binary,
			Installed: t.Available, Version: t.Version, InstallHint: t.InstallHint,
		})
	}
	return out, nil
}

// ListToolInstallMethods returns the runnable install methods for a
// managed tool on this OS.
func (a *App) ListToolInstallMethods(tool string) ([]InstallMethod, error) {
	refreshManagedPaths()
	methods := installer.ToolMethods(installer.ToolID(tool), installer.ActionInstall, installer.DetectOS())
	out := make([]InstallMethod, 0, len(methods))
	for _, m := range methods {
		out = append(out, InstallMethod{
			ID: m.ID, Label: m.Label, Command: m.Command,
			Recommended: m.Recommended, MissingPrereqs: installer.BlockingPrereqs(m, installer.RunOptions{}),
		})
	}
	return out, nil
}

// InstallManagedTool runs the chosen method, streaming output over the
// same "praimate:install" channel the CLI installs use.
func (a *App) InstallManagedTool(tool, methodID string) error {
	refreshManagedPaths()
	methods := installer.ToolMethods(installer.ToolID(tool), installer.ActionInstall, installer.DetectOS())
	for _, m := range methods {
		if m.ID == methodID {
			w := installLogWriter{ctx: a.ctx, cli: tool}
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
			defer cancel()
			err := installer.Run(ctx, m, w, w)
			refreshManagedPaths()
			return err
		}
	}
	return errUnknownMethod(tool, methodID)
}
