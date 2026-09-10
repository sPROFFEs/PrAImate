package skills

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
)

// ReleaseCheckpoint is explicit history pruning, not skill deletion. It cannot
// release the active state. Current locks/leases remain in the active catalogue.
func (s *HostSkillStore) ReleaseCheckpoint(ctx context.Context, expectedRevision uint64, id string) error {
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return err
	}
	if head.Revision != expectedRevision {
		return errors.New("skill host revision conflict")
	}
	if id == head.Current {
		return errors.New("cannot release active checkpoint")
	}
	if head.Revision == ^uint64(0) {
		return errors.New("skill host revision exhausted")
	}
	out := make([]string, 0, len(head.Retained))
	found := false
	for _, retained := range head.Retained {
		if retained == id {
			found = true
		} else {
			out = append(out, retained)
		}
	}
	if !found {
		return errors.New("checkpoint is not retained")
	}
	head.Retained = out
	head.Revision++
	return s.writeHead(ctx, head)
}

type SkillGCResult struct {
	Candidates []string
	Removed    []string
}

// Collect removes only complete generation directories not reachable from ANY
// retained host checkpoint. Publication and holds use the same process lock.
// Stage/checkpoint/backup files are not swept: retained rollback data and crash
// forensics are never inferred disposable. dryRun performs no deletion.
func (s *HostSkillStore) Collect(ctx context.Context, expectedRevision uint64, dryRun bool) (SkillGCResult, error) {
	var result SkillGCResult
	unlock, err := s.lock(ctx)
	if err != nil {
		return result, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return result, err
	}
	if head.Revision != expectedRevision {
		return result, errors.New("skill host revision conflict")
	}
	protected := make(map[string]bool)
	visited := make(map[string]*VersionCatalogue)
	for _, id := range head.Retained {
		checkpoint, err := s.checkpoint(ctx, id)
		if err != nil {
			return result, err
		}
		catalogue := visited[checkpoint.Catalogue]
		if catalogue == nil {
			catalogue, err = LoadVersionCatalogue(ctx, s.root, checkpoint.Catalogue, s.limits)
			if err != nil {
				return result, err
			}
			visited[checkpoint.Catalogue] = catalogue
		}
		// Validating host authority as well fails closed on broken retention data.
		if _, err := LoadLocalSkillState(ctx, s.root, checkpoint.LocalState, catalogue); err != nil {
			return result, err
		}
		for _, versions := range catalogue.versions {
			for _, version := range versions {
				protected[version.Generation] = true
			}
		}
	}
	directory, err := s.root.Open(".")
	if err != nil {
		return result, err
	}
	defer directory.Close()
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		entries, readErr := directory.ReadDir(1)
		if readErr != nil && readErr != io.EOF {
			return result, readErr
		}
		for _, entry := range entries {
			count++
			if count > 100000 {
				return result, errors.New("skill store inventory limit exceeded")
			}
			name := entry.Name()
			if !strings.HasPrefix(name, "install-") || len(name) != 40 {
				continue
			}
			if _, err := hex.DecodeString(name[8:]); err != nil {
				continue
			}
			if protected[name] {
				continue
			}
			if !entry.IsDir() {
				return result, errors.New("unexpected installation type during collection")
			}
			if _, err := VerifyPackageInstallation(ctx, s.root, name, s.limits); err != nil {
				return result, err
			}
			result.Candidates = append(result.Candidates, name)
		}
		if readErr == io.EOF {
			break
		}
	}
	sort.Strings(result.Candidates)
	if dryRun {
		return result, nil
	}
	for _, name := range result.Candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := s.root.RemoveAll(name); err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, name)
	}
	if len(result.Removed) > 0 {
		if err := syncPackageDirectory(s.root, "."); err != nil {
			return result, err
		}
	}
	return result, nil
}
