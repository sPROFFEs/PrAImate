package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
)

func DecodePortableSkillRecord(body []byte, dst any) error {
	if len(body) > maxRegistrySnapshot {
		return errors.New("portable skill record size limit exceeded")
	}
	if err := validateLockJSON(body); err != nil {
		return err
	}
	return decodePackageRecord(body, dst)
}

// ReadLocalArchive snapshots a selected regular file using the P1 link/race
// checks and bounded reader. Consumers never reopen the path after validation.
func ReadLocalArchive(ctx context.Context, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	base := filepath.Base(name)
	info, err := root.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("archive must be a regular non-link file")
	}
	body, _, err := readLocalPackageFile(ctx, root, base, info, limit)
	return body, err
}

// SelectionScope validates persisted preferences and their portable lock. It
// never resolves lockfile on disk or grants authority from imported fields.
func SelectionScope(config *SkillConfig, lock *SkillVersionLock) (SkillScope, error) {
	if config == nil {
		if lock != nil {
			return SkillScope{}, errors.New("skill lock requires a config")
		}
		return SkillScope{}, nil
	}
	if err := config.Validate(); err != nil {
		return SkillScope{}, err
	}
	if lock == nil {
		return SkillScope{}, errors.New("skill configuration requires its lock")
	}
	body, err := json.Marshal(lock)
	if err != nil {
		return SkillScope{}, err
	}
	if _, err := DecodeSkillVersionLock(body); err != nil {
		return SkillScope{}, err
	}
	return SkillScope{Config: cloneSkillConfig(config), Lock: body}, nil
}

func (l *SkillVersionLock) UnmarshalJSON(body []byte) error {
	value, err := DecodeSkillVersionLock(body)
	if err != nil {
		return err
	}
	*l = value
	return nil
}
func (l *SkillVersionLock) UnmarshalYAML(n *yaml.Node) error {
	count := 0
	if err := validateManifestNode(n, 0, &count); err != nil {
		return err
	}
	var value any
	if err := n.Decode(&value); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return l.UnmarshalJSON(body)
}
func (l SkillVersionLock) MarshalYAML() (any, error) {
	body, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(body))
	err = d.Decode(&value)
	return value, err
}
