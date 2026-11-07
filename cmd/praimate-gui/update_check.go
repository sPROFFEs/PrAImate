package main

// Update check and perform update bindings over internal/updater.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/updater"
	"github.com/sPROFFEs/PrAImate/internal/version"
)

// UpdateInfo is the check result for the GUI update notifications and Settings page.
type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	HasUpdate bool   `json:"hasUpdate"`
	URL       string `json:"url"`
	Edition   string `json:"edition"`
}

// CheckUpdate fetches the latest release and compares versions.
func (a *App) CheckUpdate() (*UpdateInfo, error) {
	rel, err := updater.FetchLatest()
	if err != nil {
		return nil, err
	}
	return &UpdateInfo{
		Current:   version.Current,
		Latest:    rel.TagName,
		HasUpdate: updater.IsNewer(rel.TagName, version.Current),
		URL:       rel.HTMLURL,
		Edition:   version.Edition,
	}, nil
}

// PerformUpdate downloads and applies the latest update, then restarts the application.
func (a *App) PerformUpdate() error {
	if err := updater.CheckPermissions(); err != nil {
		return err
	}
	rel, err := updater.FetchLatest()
	if err != nil {
		return err
	}
	if !updater.IsNewer(rel.TagName, version.Current) {
		return fmt.Errorf("already up to date (version %s)", version.Current)
	}
	asset, err := updater.AssetForHost(rel)
	if err != nil {
		return err
	}
	if err := updater.Apply(asset, nil); err != nil {
		return err
	}
	// A server owns active jobs and the profile lease. Leave it running until
	// an explicit, graceful restart; spawning a second owner would fail and
	// stdin/environment database credentials cannot safely be replayed here.
	if version.Edition == "server" {
		return nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return nil
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	cmd := exec.Command(exePath, os.Args[1:]...)
	cmd.Dir, _ = os.Getwd()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("restart application: %w", err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		os.Exit(0)
	}()

	return nil
}
