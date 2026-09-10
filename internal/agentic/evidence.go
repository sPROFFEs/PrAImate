package agentic

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EvidenceRequirement is an explicit artifact contract, not an assertion that
// arbitrary tests passed. It is configured by the host/workflow, never finish.
type EvidenceRequirement struct {
	Artifact string `json:"artifact" yaml:"artifact"`
	MinBytes int64  `json:"min_bytes,omitempty" yaml:"min_bytes,omitempty"`
	SHA256   string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
}

func ValidateEvidenceRequirements(requirements []EvidenceRequirement) error {
	if len(requirements) > 32 {
		return errors.New("too many finish evidence requirements")
	}
	seen := map[string]bool{}
	for _, r := range requirements {
		if r.Artifact == "" || filepath.Base(r.Artifact) != r.Artifact || strings.Trim(artifactNameRE.ReplaceAllString(r.Artifact, "-"), "-.") != r.Artifact || seen[r.Artifact] || r.MinBytes < 0 || r.MinBytes > 8<<20 {
			return errors.New("invalid or duplicate evidence artifact requirement")
		}
		seen[r.Artifact] = true
		if r.SHA256 != "" {
			if len(r.SHA256) != 64 || strings.ToLower(r.SHA256) != r.SHA256 {
				return errors.New("evidence SHA256 must be 64 lowercase hex characters")
			}
			if _, err := hex.DecodeString(r.SHA256); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyFinishEvidence(store artifactStore, instance *Instance, requirements []EvidenceRequirement) error {
	for _, required := range requirements {
		observed := false
		for _, artifact := range instance.Artifacts {
			if artifact.Name == required.Artifact {
				observed = true
			}
		}
		if !observed {
			return fmt.Errorf("finish evidence missing: %s must be written through artifact.write in this run", required.Artifact)
		}
		root, err := os.OpenRoot(store.dir)
		if err != nil {
			return err
		}
		info, err := root.Lstat(required.Artifact)
		if err != nil || !info.Mode().IsRegular() {
			root.Close()
			return fmt.Errorf("finish evidence unavailable or not a regular file: %s", required.Artifact)
		}
		f, err := root.Open(required.Artifact)
		if err != nil {
			root.Close()
			return err
		}
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			f.Close()
			root.Close()
			return errors.New("finish evidence changed while opening")
		}
		body, err := io.ReadAll(io.LimitReader(f, int64(store.maxSize)+1))
		f.Close()
		root.Close()
		if err != nil {
			return err
		}
		if len(body) == 0 || int64(len(body)) < required.MinBytes || len(body) > store.maxSize {
			return fmt.Errorf("finish evidence size invalid: %s", required.Artifact)
		}
		hash := sha256.Sum256(body)
		if required.SHA256 != "" && hex.EncodeToString(hash[:]) != required.SHA256 {
			return fmt.Errorf("finish evidence hash mismatch: %s", required.Artifact)
		}
	}
	return nil
}
