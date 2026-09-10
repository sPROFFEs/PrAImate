package main

import (
	"encoding/json"
	"errors"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// SkillBindingPreview describes resolution, never runtime delivery.
type SkillBindingPreview struct {
	Scope      string                  `json:"scope"`
	Resolution skills.ResolvedSkillSet `json:"resolution"`
	Error      string                  `json:"error,omitempty"`
}

func (a *App) InstalledSkillVersionsV2() ([]core.InstalledSkillSummary, error) {
	if a.detachedClient != nil {
		var out []core.InstalledSkillSummary
		err := a.detachedClient.rpc("skills.v2.versions", nil, &out)
		return out, err
	}
	if _, err := a.requireCore(); err != nil {
		return nil, err
	}
	return core.InstalledSkillSummaries(a.ctx)
}

func (a *App) BuildInstalledSkillSelectionV2(body string) (core.SkillSelectionInput, error) {
	if a.detachedClient != nil {
		var out core.SkillSelectionInput
		err := a.detachedClient.rpc("skills.v2.build", body, &out)
		return out, err
	}
	c, err := a.requireCore()
	if err != nil {
		return core.SkillSelectionInput{}, err
	}
	if len(body) > 1<<20 {
		return core.SkillSelectionInput{}, errors.New("selection size limit exceeded")
	}
	var choices []core.InstalledSkillChoice
	if err := skills.DecodePortableSkillRecord([]byte(body), &choices); err != nil {
		return core.SkillSelectionInput{}, err
	}
	return c.BuildInstalledSkillSelection(a.ctx, choices)
}

// SaveChatSkillChoicesV2 is the normal UI path: choices become a verified,
// approved, immutable lock and are persisted as one host operation. The lower
// level build/set methods remain for detached RPC compatibility and tests.
func (a *App) SaveChatSkillChoicesV2(chatID, body string) (core.SkillSelectionInput, error) {
	selection, err := a.BuildInstalledSkillSelectionV2(body)
	if err != nil {
		return core.SkillSelectionInput{}, err
	}
	encoded, err := json.Marshal(selection)
	if err != nil {
		return core.SkillSelectionInput{}, err
	}
	if err := a.SetChatSkillsV2(chatID, string(encoded)); err != nil {
		return core.SkillSelectionInput{}, err
	}
	return selection, nil
}

func bindingPreview(scope string, result skills.ResolvedSkillSet, err error) SkillBindingPreview {
	p := SkillBindingPreview{Scope: scope, Resolution: result}
	if err != nil {
		p.Error = err.Error()
		var resolutionError *skills.SkillResolutionError
		if errors.As(err, &resolutionError) {
			p.Resolution.Diagnostics = resolutionError.Diagnostics
		}
	}
	return p
}

func (a *App) PreviewAgentSkillsV2(body string) ([]SkillBindingPreview, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := core.ParseAgentYAML(strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	result, err := c.PreviewNewBoundSkills(a.ctx, agent, nil, core.ChatSettings{})
	out := []SkillBindingPreview{bindingPreview("agent", result, err)}
	for i := range agent.Workflows {
		result, err = c.PreviewNewBoundSkills(a.ctx, agent, &agent.Workflows[i], core.ChatSettings{})
		out = append(out, bindingPreview("workflow:"+agent.Workflows[i].Name, result, err))
	}
	return out, nil
}

func (a *App) SkillsV2RolloutState() (core.SkillsV2Rollout, error) {
	c, err := a.requireCore()
	if err != nil {
		return core.SkillsV2Rollout{}, err
	}
	return c.SkillsV2RolloutState(a.ctx)
}

func (a *App) SetSkillsV2RolloutState(enabled bool) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.SetSkillsV2RolloutState(a.ctx, enabled)
}

func (a *App) ChatSkillsV2(chatID string) (core.SkillSelectionInput, error) {
	if a.detachedClient != nil {
		var out core.SkillSelectionInput
		if chatID != a.detachedClient.mode.sessionID {
			return out, errors.New("chat is outside this detached window")
		}
		err := a.detachedClient.rpc("chat.skills.v2", nil, &out)
		return out, err
	}
	c, err := a.requireCore()
	if err != nil {
		return core.SkillSelectionInput{}, err
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return core.SkillSelectionInput{}, err
	}
	return core.SkillSelectionInput{Config: chat.Settings.SkillsV2, Lock: chat.Settings.SkillsLock}, nil
}

func (a *App) PreviewChatSkillsV2(chatID, body string) (SkillBindingPreview, error) {
	if a.detachedClient != nil {
		var out SkillBindingPreview
		if chatID != a.detachedClient.mode.sessionID {
			return out, errors.New("chat is outside this detached window")
		}
		err := a.detachedClient.rpc("chat.skills.v2.preview", body, &out)
		return out, err
	}
	c, err := a.requireCore()
	if err != nil {
		return SkillBindingPreview{}, err
	}
	selection, err := core.ParseSkillSelectionInput([]byte(body))
	if err != nil {
		return SkillBindingPreview{}, err
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return SkillBindingPreview{}, err
	}
	chat.Settings.SkillsV2, chat.Settings.SkillsLock = selection.Config, selection.Lock
	if selection.Config != nil {
		chat.Settings.Skills = nil
	}
	result, err := c.PreviewBoundSkills(a.ctx, nil, nil, chat.Settings)
	return bindingPreview("session", result, err), nil
}

func (a *App) SetChatSkillsV2(chatID, body string) error {
	if a.detachedClient != nil {
		if chatID != a.detachedClient.mode.sessionID {
			return errors.New("chat is outside this detached window")
		}
		return a.detachedClient.rpc("chat.skills.v2.set", body, nil)
	}
	a.chatCancelMu.Lock()
	_, active := a.chatCancels[chatID]
	a.chatCancelMu.Unlock()
	if active {
		return errors.New("wait for the current turn or stop it before changing skill bindings")
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	selection, err := core.ParseSkillSelectionInput([]byte(body))
	if err != nil {
		return err
	}
	if selection.Config != nil {
		state, err := c.SkillsV2RolloutState(a.ctx)
		if err != nil {
			return err
		}
		if !state.Enabled {
			return errors.New("skills_v2_disabled: enable controlled v2 skills in Skills before saving this selection")
		}
	}
	return c.UpdateChatSettings(a.ctx, chatID, func(s *core.ChatSettings) {
		s.SkillsV2, s.SkillsLock = selection.Config, selection.Lock
		if selection.Config != nil {
			s.Skills = nil
		}
	})
}
