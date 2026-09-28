package main

import (
	"context"
	"fmt"
	"unicode/utf8"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func (a *App) ListManagedRuns(agentID string) ([]core.ManagedRun, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ListManagedRuns(agentID)
}

func (a *App) ResumeManagedRun(runID string) (*core.ManagedRun, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.managedCancelMu.Lock()
	if old := a.managedCancels[runID]; old != nil {
		old()
	}
	a.managedCancels[runID] = cancel
	a.managedCancelMu.Unlock()
	defer func() {
		cancel()
		a.managedCancelMu.Lock()
		delete(a.managedCancels, runID)
		a.managedCancelMu.Unlock()
	}()
	return c.ResumeManagedRun(ctx, runID, func(event core.ManagedRunEvent) {
		wruntime.EventsEmit(a.ctx, "praimate:managed-run", event)
	})
}

func (a *App) CancelManagedRun(runID string) {
	a.managedCancelMu.Lock()
	cancel := a.managedCancels[runID]
	a.managedCancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) ManagedRunDetails(runID string) (*core.ManagedRun, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.GetManagedRun(runID)
}

// ManagedArtifactText returns an artifact for inspection in the GUI. The 1.4
// broker creates text artifacts only; rejecting non-UTF-8 here keeps binary
// data out of the Wails JSON bridge if future brokers add screenshots/files.
func (a *App) ManagedArtifactText(runID, name string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	body, err := c.ReadManagedArtifact(runID, name)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(body) {
		return "", fmt.Errorf("artifact %q is binary and cannot be previewed as text", name)
	}
	return string(body), nil
}
