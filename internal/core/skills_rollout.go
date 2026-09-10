package core

import (
	"context"
	"encoding/json"
	"errors"
)

const skillsV2RolloutSetting = "skills_v2_rollout"

// SkillsV2Rollout is a local, reversible cohort switch. It controls only the
// versioned/controlled skills path: legacy skill selections keep their prior
// behaviour and a disabled v2 selection is rejected rather than downgraded.
// The setting is deliberately local to the encrypted PrAImate database; it is
// neither exported in agent packs nor inferred from imported package content.
type SkillsV2Rollout struct {
	Enabled bool `json:"enabled"`
}

// SkillsV2RolloutState returns disabled when no user has opted in. Keeping the
// default off avoids changing legacy users' execution defaults on upgrade.
func (c *Core) SkillsV2RolloutState(ctx context.Context) (SkillsV2Rollout, error) {
	raw, err := c.GetSetting(ctx, ScopeCLI, skillsV2RolloutSetting)
	if err != nil || len(raw) == 0 {
		return SkillsV2Rollout{}, err
	}
	var state SkillsV2Rollout
	if err := json.Unmarshal(raw, &state); err != nil {
		return SkillsV2Rollout{}, errors.New("invalid local v2 skills rollout setting")
	}
	return state, nil
}

// SetSkillsV2RolloutState changes the local activation gate. Enabling also
// registers and approves the exact built-in packages shipped by this binary;
// it never changes agent/chat locks or external approvals. Disabling remains
// an immediate runtime rollback and preserves every pinned selection.
func (c *Core) SetSkillsV2RolloutState(ctx context.Context, enabled bool) error {
	if enabled {
		if err := EnsureBuiltinSkillsV2(ctx); err != nil {
			return err
		}
	}
	body, err := json.Marshal(SkillsV2Rollout{Enabled: enabled})
	if err != nil {
		return err
	}
	return c.SetSetting(ctx, ScopeCLI, skillsV2RolloutSetting, body)
}

func (c *Core) requireSkillsV2Rollout(ctx context.Context) error {
	state, err := c.SkillsV2RolloutState(ctx)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return errors.New("skills_v2_disabled: controlled v2 skills are disabled for this local cohort; enable them in Skills before running this selection")
	}
	return nil
}
