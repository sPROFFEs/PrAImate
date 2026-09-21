package main

import (
	"fmt"
	"path/filepath"

	"git.jtsec.local/lab/PrAImate/internal/studio"
)

// StudioGetStatus returns the installation and daemon status for PrAImate Studio.
func (a *App) StudioGetStatus() (*studio.StudioStatus, error) {
	mgr := studio.NewManager(a.core)
	return mgr.GetStatus(a.ctx)
}

// StudioInstall provisions the built-in extension and environment for PrAImate Studio.
func (a *App) StudioInstall() (*studio.StudioStatus, error) {
	mgr := studio.NewManager(a.core)
	return mgr.Install(a.ctx)
}

// StudioUpdate refreshes the Studio extension and scripts.
func (a *App) StudioUpdate() (*studio.StudioStatus, error) {
	mgr := studio.NewManager(a.core)
	return mgr.Update(a.ctx)
}

// StudioRepair repairs the Studio environment and permissions.
func (a *App) StudioRepair() (*studio.StudioStatus, error) {
	mgr := studio.NewManager(a.core)
	return mgr.Repair(a.ctx)
}

// StudioOpenProject launches PrAImate Studio with the given configuration.
func (a *App) StudioOpenProject(workspacePath, cli, model, agentID, tools string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	refreshManagedPaths()
	mgr := studio.NewManager(c)
	target := workspacePath
	if target == "" {
		target = "."
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve workspace path: %w", err)
	}

	_ = mgr.SaveRecentProject(abs)

	return mgr.Launch(a.ctx, studio.LaunchOptions{
		WorkspacePath: abs,
		CLI:           cli,
		Model:         model,
		AgentID:       agentID,
		Tools:         tools,
	})
}

// StudioListRecentProjects returns recent workspace folders opened in Studio.
func (a *App) StudioListRecentProjects() ([]string, error) {
	mgr := studio.NewManager(a.core)
	return mgr.ListRecentProjects()
}
