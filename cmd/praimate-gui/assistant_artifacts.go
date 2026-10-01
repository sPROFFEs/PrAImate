package main

import (
	"context"
	"errors"

	"github.com/sPROFFEs/PrAImate/internal/artifacts"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/studio"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) AssistantArtifactCatalog() (*core.ManagedArtifactCatalog, error) {
	if a.core == nil {
		return nil, errors.New("unlock the database first")
	}
	return a.core.ManagedArtifactCatalog()
}
func (a *App) InstallAssistantArtifact(id string) (*artifacts.Installation, error) {
	if a.core == nil {
		return nil, errors.New("unlock the database first")
	}
	return a.core.InstallManagedArtifact(context.Background(), id, func(p artifacts.Progress) {
		studio.PublishDesktopAssistant(a.core, "assistant.artifact.progress", p)
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "assistant:artifact-progress", p)
		}
	})
}
func (a *App) VerifyAssistantArtifact(id string) (*artifacts.Installation, error) {
	if a.core == nil {
		return nil, errors.New("unlock the database first")
	}
	return a.core.VerifyManagedArtifact(context.Background(), id)
}
func (a *App) RemoveAssistantArtifact(id string) error {
	if a.core == nil {
		return errors.New("unlock the database first")
	}
	return a.core.RemoveManagedArtifact(id)
}
func (a *App) CancelAssistantArtifactInstall() {
	if a.core != nil {
		a.core.CancelManagedArtifactInstall()
	}
}
