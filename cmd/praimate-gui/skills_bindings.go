package main

// Skills page Wails bindings — browse the built-in catalogue, manage
// the "default skills for new chats" list, and flip per-chat
// enablement on existing chats. Skills are CLI-tagged in the catalogue
// but injected into the system prompt regardless of CLI — the page
// warns the user when a skill is enabled on a chat whose CLI it
// wasn't designed for.

import (
	"encoding/json"
	"os"
	"path/filepath"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/core"
)

// SkillsList returns the combined catalogue (built-ins + user-added).
func (a *App) SkillsList() []core.Skill {
	return core.SkillCatalogue()
}

// SkillsUserList returns only the user-installed skills (for the
// "your skills" section of the page).
func (a *App) SkillsUserList() []core.Skill {
	return core.LoadUserSkills()
}

// AddUserSkillInput is the input payload for AddUserSkill.
type AddUserSkillInput struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	CLIs        []string `json:"clis"`
	Body        string   `json:"body"`
	Source      string   `json:"source"`
}

// AddUserSkill inserts (or updates by id) a user skill. CLIs may be
// empty to mark the skill universal.
func (a *App) AddUserSkill(in AddUserSkillInput) (*core.Skill, error) {
	return core.AddUserSkill(core.Skill{
		ID:          in.ID,
		Name:        in.Name,
		Description: in.Description,
		CLIs:        in.CLIs,
		Body:        in.Body,
		Source:      in.Source,
	})
}

// DeleteUserSkill removes a user skill by id. No-op for built-in ids.
func (a *App) DeleteUserSkill(id string) error {
	return core.DeleteUserSkill(id)
}

// ImportSkillFromURL fetches a skill body from a URL (markdown file
// or ZIP of markdown files). The user fills in CLIs + name before
// calling AddUserSkill.
func (a *App) ImportSkillFromURL(rawURL string) (*core.Skill, error) {
	return core.ImportSkillFromURL(a.ctx, rawURL)
}

// ImportSkillFromZipFile reads a local ZIP and returns a seeded skill
// the user can finalise via AddUserSkill.
func (a *App) ImportSkillFromZipFile(path string) (*core.Skill, error) {
	return core.ImportSkillFromZipFile(path)
}

// PickSkillZipFile opens a native file picker scoped to .zip files.
// Returns "" if the user cancels.
func (a *App) PickSkillZipFile() (string, error) {
	return wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Choose a skill ZIP",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Skill ZIPs (*.zip)", Pattern: "*.zip"},
		},
	})
}

// SkillsForCLI returns the catalogue filtered to entries that target
// the given CLI. The Skills page uses this for the per-CLI tabs.
func (a *App) SkillsForCLI(cli string) []core.Skill {
	return core.SkillsForCLI(cli)
}

// --- default skills (global) ---

// defaultsFile is where the "default skills for new chats" list lives —
// a plain JSON file alongside the praimate config. Kept separate from
// the SQLite store so the file is greppable / hand-editable.
func defaultsFile() (string, error) {
	dir, err := appdata.Root()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "default-skills.json"), nil
}

type defaultsBody struct {
	Skills []string `json:"skills"`
}

// SkillsDefaults returns the IDs of skills that get auto-enabled on
// every newly-created chat. Returns an empty slice when the file
// hasn't been written yet.
func (a *App) SkillsDefaults() []string {
	path, err := defaultsFile()
	if err != nil {
		return []string{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}
	var body defaultsBody
	if err := json.Unmarshal(b, &body); err != nil {
		return []string{}
	}
	if body.Skills == nil {
		return []string{}
	}
	return body.Skills
}

// SetSkillsDefaults overwrites the default-skills list.
func (a *App) SetSkillsDefaults(ids []string) error {
	path, err := defaultsFile()
	if err != nil {
		return err
	}
	if ids == nil {
		ids = []string{}
	}
	body, err := json.MarshalIndent(defaultsBody{Skills: ids}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

// --- per-chat enablement ---

// ChatSkills returns the IDs of skills currently enabled on a chat.
func (a *App) ChatSkills(chatID string) []string {
	if a.detachedClient != nil {
		return a.detachedClient.chatSkills(chatID)
	}
	c, err := a.requireCore()
	if err != nil {
		return []string{}
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return []string{}
	}
	if chat.Settings.Skills == nil {
		return []string{}
	}
	return chat.Settings.Skills
}

// SetChatSkills overwrites the skills list on a chat.
func (a *App) SetChatSkills(chatID string, ids []string) error {
	if a.detachedClient != nil {
		return a.detachedClient.setChatSkills(chatID, ids)
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.UpdateChatSettings(a.ctx, chatID, func(s *core.ChatSettings) {
		if ids == nil {
			s.Skills = nil
			return
		}
		s.Skills = append([]string(nil), ids...)
	})
}
