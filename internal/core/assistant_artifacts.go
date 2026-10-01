package core

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
	"github.com/sPROFFEs/PrAImate/internal/artifacts"
)

type ManagedArtifact struct {
	artifacts.Definition
	Installed bool `json:"installed"`
}

type ManagedArtifactCatalog struct {
	DistributionReady bool                `json:"distribution_ready"`
	ManifestURL       string              `json:"manifest_url"`
	Items             []ManagedArtifact   `json:"items"`
	Notice            string              `json:"notice,omitempty"`
	Active            bool                `json:"active"`
	Progress          *artifacts.Progress `json:"progress,omitempty"`
}

func (c *Core) artifactManager() (*artifacts.Service, error) {
	c.artifactMu.Lock()
	defer c.artifactMu.Unlock()
	if c.artifacts != nil {
		return c.artifacts, nil
	}
	root, err := appdata.Root()
	if err != nil {
		return nil, err
	}
	c.artifacts, err = artifacts.New(artifacts.Options{Root: filepath.Join(root, "artifacts")})
	return c.artifacts, err
}

func (c *Core) ManagedArtifactCatalog() (*ManagedArtifactCatalog, error) {
	s, err := c.artifactManager()
	if err != nil {
		return nil, err
	}
	result := &ManagedArtifactCatalog{DistributionReady: true, ManifestURL: artifacts.ManifestURL, Items: []ManagedArtifact{}}
	c.artifactMu.Lock()
	result.Active = c.artifactCancel != nil
	if c.artifactProgress != nil {
		p := *c.artifactProgress
		result.Progress = &p
	}
	c.artifactMu.Unlock()
	for _, d := range artifacts.Catalog() {
		result.Items = append(result.Items, ManagedArtifact{Definition: d, Installed: s.Installed(d.ArtifactID)})
	}
	return result, nil
}

func knownArtifact(id string) (*artifacts.Definition, error) {
	for _, d := range artifacts.Catalog() {
		if d.ArtifactID == id {
			return &d, nil
		}
	}
	return nil, errors.New("artifact is not in the supported model/runtime catalog")
}

// InstallManagedArtifact downloads only the selected model and its required
// runtime. It never enables Assistant/Voice or chooses models from hardware.
func (c *Core) InstallManagedArtifact(ctx context.Context, id string, emit func(artifacts.Progress)) (*artifacts.Installation, error) {
	d, err := knownArtifact(id)
	if err != nil {
		return nil, err
	}
	s, err := c.artifactManager()
	if err != nil {
		return nil, err
	}
	c.artifactMu.Lock()
	if c.artifactClosed {
		c.artifactMu.Unlock()
		return nil, errors.New("model installer is shutting down")
	}
	if c.artifactCancel != nil {
		c.artifactMu.Unlock()
		return nil, errors.New("another model operation is active")
	}
	ctx, cancel := context.WithCancel(ctx)
	c.artifactCancel = cancel
	c.artifactDone = make(chan struct{})
	c.artifactProgress = nil
	c.artifactMu.Unlock()
	progress := func(p artifacts.Progress) {
		c.artifactMu.Lock()
		c.artifactProgress = &p
		c.artifactMu.Unlock()
		if emit != nil {
			emit(p)
		}
	}
	defer func() {
		cancel()
		c.artifactMu.Lock()
		c.artifactCancel = nil
		close(c.artifactDone)
		c.artifactDone = nil
		c.artifactMu.Unlock()
	}()
	// A runtime failure does not leave a model falsely reported as usable.
	if d.RuntimeID != "" {
		if _, err := s.Install(ctx, d.RuntimeID, progress); err != nil {
			return nil, err
		}
	}
	return s.Install(ctx, id, progress)
}

func (c *Core) CancelManagedArtifactInstall() {
	c.artifactMu.Lock()
	defer c.artifactMu.Unlock()
	if c.artifactCancel != nil {
		c.artifactCancel()
	}
}

// StopManagedArtifactInstalls closes this Core's installer and waits for its
// filesystem writes/cleanup to finish before shutdown or a full data reset.
func (c *Core) StopManagedArtifactInstalls(ctx context.Context) error {
	c.artifactMu.Lock()
	c.artifactClosed = true
	if c.artifactCancel != nil {
		c.artifactCancel()
	}
	done := c.artifactDone
	c.artifactMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Core) VerifyManagedArtifact(ctx context.Context, id string) (*artifacts.Installation, error) {
	d, err := knownArtifact(id)
	if err != nil {
		return nil, err
	}
	s, err := c.artifactManager()
	if err != nil {
		return nil, err
	}
	if d.RuntimeID != "" {
		if _, err := s.Verify(ctx, d.RuntimeID); err != nil {
			return nil, err
		}
	}
	return s.Verify(ctx, id)
}

func (c *Core) ManagedModelPaths(ctx context.Context, id string) (model, runtime *artifacts.Installation, err error) {
	d, err := knownArtifact(id)
	if err != nil {
		return nil, nil, err
	}
	if d.RuntimeID == "" {
		return nil, nil, errors.New("select a model with an inference runtime")
	}
	s, err := c.artifactManager()
	if err != nil {
		return nil, nil, err
	}
	runtime, err = s.Verify(ctx, d.RuntimeID)
	if err != nil {
		return nil, nil, err
	}
	if runtime.Artifact.Format == "file" {
		return nil, nil, errors.New("runtime must be a verified executable archive")
	}
	model, err = s.Verify(ctx, id)
	return model, runtime, err
}

func (c *Core) RemoveManagedArtifact(id string) error {
	d, err := knownArtifact(id)
	if err != nil {
		return err
	}
	s, err := c.artifactManager()
	if err != nil {
		return err
	}
	if d.Kind == "runtime" {
		for _, model := range artifacts.Catalog() {
			if model.RuntimeID == id && s.Installed(model.ArtifactID) {
				return errors.New("remove the models using this runtime first")
			}
		}
	}
	return s.Remove(id)
}
