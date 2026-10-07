package agentic

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Artifact struct {
	SourceRunID string    `json:"sourceRunId,omitempty"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"createdAt"`
}

var artifactNameRE = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type artifactStore struct {
	dir     string
	maxSize int
}

func (s artifactStore) write(raw json.RawMessage) (Artifact, error) {
	var args struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decodeArguments(raw, &args); err != nil {
		return Artifact{}, err
	}
	return s.writeText(args.Name, args.Content)
}

func (s artifactStore) writeText(name, content string) (Artifact, error) {
	name = strings.Trim(artifactNameRE.ReplaceAllString(filepath.Base(strings.TrimSpace(name)), "-"), "-.")
	if name == "" {
		return Artifact{}, errors.New("artifact name needs at least one letter or digit")
	}
	if len(content) > s.maxSize {
		return Artifact{}, fmt.Errorf("artifact %q exceeds the %d-byte limit", name, s.maxSize)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(s.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: name, Size: int64(len(content)), CreatedAt: time.Now().UTC()}, nil
}
