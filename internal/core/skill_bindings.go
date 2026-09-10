package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

func agentSkillScopes(a *Agent) []skills.SkillScope {
	if a == nil {
		return nil
	}
	out := []skills.SkillScope{}
	if a.Skills != nil {
		s, _ := skills.SelectionScope(a.Skills, a.SkillsLock)
		out = append(out, s)
	}
	for _, w := range a.Workflows {
		if w.Skills != nil {
			s, _ := skills.SelectionScope(w.Skills, w.SkillsLock)
			out = append(out, s)
		}
	}
	return out
}
func chatSkillScope(s ChatSettings) skills.SkillScope {
	scope, _ := skills.SelectionScope(s.SkillsV2, s.SkillsLock)
	return scope
}

func freezeBoundSettings(a *Agent, w *Workflow, s ChatSettings) (ChatSettings, error) {
	if err := validateChatSkills(s); err != nil {
		return s, err
	}
	scopes := skills.SkillScopes{Session: chatSkillScope(s)}
	if a != nil {
		var err error
		scopes.Agent, err = skills.SelectionScope(a.Skills, a.SkillsLock)
		if err != nil {
			return s, err
		}
	}
	if w != nil {
		var err error
		scopes.Workflow, err = skills.SelectionScope(w.Skills, w.SkillsLock)
		if err != nil {
			return s, err
		}
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return s, err
	}
	if frozen.Config != nil {
		l, err := skills.DecodeSkillVersionLock(frozen.Lock)
		if err != nil {
			return s, err
		}
		s.SkillsV2 = frozen.Config
		s.SkillsLock = &l
	}
	return s, validateChatSkills(s)
}
func validateChatSkills(s ChatSettings) error {
	if s.SkillsV2 != nil && len(s.Skills) > 0 {
		return errors.New("legacy and v2 skill selections cannot be combined")
	}
	_, err := skills.SelectionScope(s.SkillsV2, s.SkillsLock)
	return err
}

// Retention is durable BEFORE DB publication. A failed DB write can leave a
// conservative hold, never a reference eligible for GC. Each immutable entity
// selection has its own hold; updates cannot release another process's pin.
func (c *Core) retainSkillSelections(ctx context.Context, entity string, scopes []skills.SkillScope) error {
	var entries []skills.SkillVersionLockEntry
	for _, scope := range scopes {
		if scope.Config == nil {
			continue
		}
		lock, err := skills.DecodeSkillVersionLock(scope.Lock)
		if err != nil {
			return err
		}
		entries = append(entries, lock.Entries...)
	}
	if len(entries) == 0 {
		return nil
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte(entity+"\x00"), body...))
	owner := "binding:" + hex.EncodeToString(sum[:])
	s, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return err
	}
	defer s.Close()
	view, err := s.View(ctx)
	if err != nil {
		return err
	}
	_, err = s.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		var versions []skills.SkillVersion
		seen := map[string]bool{}
		for _, e := range entries {
			v, _, err := tx.ResolveVersion(ctx, e.Ref, e.Digest)
			if err != nil {
				return err
			}
			if v.SourceID != e.SourceID {
				return errors.New("skill binding identity mismatch")
			}
			// Use the lock boundary to verify provenance without granting approval.
			expected, err := tx.ExportLock(ctx, []skills.SkillVersion{v})
			if err != nil {
				return err
			}
			l, err := skills.DecodeSkillVersionLock(expected)
			if err != nil {
				return err
			}
			a, _ := json.Marshal(e)
			b, _ := json.Marshal(l.Entries[0])
			if string(a) != string(b) {
				return errors.New("skill binding provenance mismatch")
			}
			key := v.SourceID + v.Digest
			if !seen[key] {
				versions = append(versions, v)
				seen[key] = true
			}
		}
		return tx.Retain(ctx, owner, versions)
	})
	return err
}

func (c *Core) SkillDefaultsV2(ctx context.Context) (*skills.SkillConfig, *skills.SkillVersionLock, error) {
	if c.store == nil {
		return nil, nil, nil
	}
	var config, lock string
	err := c.store.DB().QueryRowContext(ctx, "SELECT config_json, lock_json FROM skill_defaults WHERE id=1").Scan(&config, &lock)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var cfg *skills.SkillConfig
	var l *skills.SkillVersionLock
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal([]byte(lock), &l); err != nil {
		return nil, nil, err
	}
	_, err = skills.SelectionScope(cfg, l)
	return cfg, l, err
}
func (c *Core) SetSkillDefaultsV2(ctx context.Context, config *skills.SkillConfig, lock *skills.SkillVersionLock) error {
	if c.store == nil {
		return errors.New("skill defaults require an open store")
	}
	if config != nil {
		if err := c.requireSkillsV2Rollout(ctx); err != nil {
			return err
		}
	}
	scope, err := skills.SelectionScope(config, lock)
	if err != nil {
		return err
	}
	if err := c.retainSkillSelections(ctx, "defaults", []skills.SkillScope{scope}); err != nil {
		return err
	}
	cfg, _ := json.Marshal(config)
	l, _ := json.Marshal(lock)
	_, err = c.store.DB().ExecContext(ctx, "INSERT INTO skill_defaults(id,config_json,lock_json) VALUES(1,?,?) ON CONFLICT(id) DO UPDATE SET config_json=excluded.config_json, lock_json=excluded.lock_json", string(cfg), string(l))
	return err
}
func (c *Core) snapshotChatSkills(ctx context.Context, req *CreateChatRequest) error {
	if err := validateChatSkills(req.Settings); err != nil {
		return err
	}
	cfg, lock, err := c.SkillDefaultsV2(ctx)
	if err != nil {
		return err
	}
	app, _ := skills.SelectionScope(cfg, lock)
	scopes := skills.SkillScopes{Application: app, Session: chatSkillScope(req.Settings)}
	if req.AgentID != "" {
		a, err := c.GetAgent(ctx, req.AgentID)
		if err != nil && !errors.Is(err, ErrAgentNotFound) {
			return err
		}
		// Legacy headless workflows can use ephemeral agents not stored in DB.
		// Their caller passes any explicit v2 workflow snapshot in Settings.
		if a != nil {
			scopes.Agent, _ = skills.SelectionScope(a.Skills, a.SkillsLock)
		}
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return err
	}
	if frozen.Config != nil {
		if err := c.requireSkillsV2Rollout(ctx); err != nil {
			return err
		}
		l, err := skills.DecodeSkillVersionLock(frozen.Lock)
		if err != nil {
			return err
		}
		req.Settings.SkillsV2 = frozen.Config
		req.Settings.SkillsLock = &l
	}
	return validateChatSkills(req.Settings)
}

func (c *Core) PreviewBoundSkills(ctx context.Context, a *Agent, w *Workflow, settings ChatSettings) (skills.ResolvedSkillSet, error) {
	if err := validateChatSkills(settings); err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	scopes := skills.SkillScopes{Session: chatSkillScope(settings)}
	if a != nil {
		var err error
		scopes.Agent, err = skills.SelectionScope(a.Skills, a.SkillsLock)
		if err != nil {
			return skills.ResolvedSkillSet{}, err
		}
	}
	if w != nil {
		scope, err := skills.SelectionScope(w.Skills, w.SkillsLock)
		if err != nil {
			return skills.ResolvedSkillSet{}, err
		}
		scopes.Workflow = scope
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	if frozen.Config == nil {
		return skills.ResolvedSkillSet{Legacy: true}, nil
	}
	if err := c.requireSkillsV2Rollout(ctx); err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	return PreviewSkillResolution(ctx, scopes, skills.SkillResolutionPolicy{Budget: skills.SkillBudget{CatalogTokens: 1000, BodyTokens: 4000, ResourceTokens: 2000, TotalTokens: 6000, MaxActive: 3, MaxLoadCallsPerTurn: 4, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 16384}, Transport: "controlled", RequireControlled: true})
}

// P3 persists/validates selections; delivery is intentionally unavailable until
// P4-P6. Never execute a required v2 binding through the legacy prefix path.
func (c *Core) guardBoundSkills(ctx context.Context, a *Agent, w *Workflow, s ChatSettings) error {
	if (a == nil || a.Skills == nil) && (w == nil || w.Skills == nil) && s.SkillsV2 == nil {
		return nil
	}
	if err := c.requireSkillsV2Rollout(ctx); err != nil {
		return err
	}
	result, err := c.PreviewBoundSkills(ctx, a, w, s)
	if err != nil {
		return err
	}
	_ = result
	return nil
}
