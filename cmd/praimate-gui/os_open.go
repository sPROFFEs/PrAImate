package main

// "Reveal in file manager" bindings — used by the Studio editor and the
// New-Agent / agent-knowledge studios for the "open workspace path in
// the system file explorer" button.

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func openPathInFileManager(dir string) error {
	if dir == "" {
		return errors.New("no path")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s %s: %w", cmd.Path, dir, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// OpenEditorFolder reveals the studio's working folder in the OS file
// manager. Only valid in editor mode (the second-process Studio window).
func (a *App) OpenEditorFolder() error {
	if a.detachedClient != nil && a.detachedClient.mode.kind == "studio" {
		return a.detachedClient.openEditorFolder()
	}
	if editorFolder == "" {
		return errors.New("not an editor window")
	}
	return openPathInFileManager(editorFolder)
}

// OpenAgentKnowledgeFolder reveals the agent's knowledge directory in the
// OS file manager. Creates the directory on demand so the button works
// even before the user has enabled knowledge — the empty folder is a
// fine drop target.
func (a *App) OpenAgentKnowledgeFolder(id string) error {
	dir, err := core.AgentKnowledgeDir(id)
	if err != nil {
		return err
	}
	return openPathInFileManager(dir)
}
