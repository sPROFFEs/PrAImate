package core

import (
	"context"
	"encoding/json"
	"errors"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// PreviewLegacySkillMigration adapts the actual built-in and skills.json shapes
// to the common package service. Caller supplies a bounded read of skills.json;
// an absent file is nil. This does not touch files, defaults, chats or agents.
func PreviewLegacySkillMigration(ctx context.Context, userCatalogue []byte, limits skills.PackageLimits) (*skills.LegacyMigrationPlan, error) {
	if len(userCatalogue) > 4<<20 {
		return nil, errors.New("legacy catalogue exceeds migration limit")
	}
	var entries []skills.LegacyInlineSkill
	for _, entry := range BuiltinSkillCatalogue() {
		entries = append(entries, skills.LegacyInlineSkill{ID: entry.ID, Name: entry.Name, Description: entry.Description, Body: entry.Body, CLIs: entry.CLIs, Builtin: true})
	}
	if len(userCatalogue) > 0 {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(userCatalogue, &fields); err != nil || fields["skills"] == nil {
			return nil, errors.New("legacy catalogue requires a skills array; original must be preserved")
		}
		var catalogue struct {
			Skills []json.RawMessage `json:"skills"`
		}
		if err := json.Unmarshal(userCatalogue, &catalogue); err != nil {
			return nil, errors.New("invalid legacy catalogue JSON; original must be preserved")
		}
		for _, raw := range catalogue.Skills {
			var entry Skill
			if err := json.Unmarshal(raw, &entry); err != nil {
				entries = append(entries, skills.LegacyInlineSkill{Invalid: "malformed user skill entry"})
				continue
			}
			entries = append(entries, skills.LegacyInlineSkill{ID: entry.ID, Name: entry.Name, Description: entry.Description, Body: entry.Body, CLIs: entry.CLIs})
		}
	}
	return skills.PrepareLegacyMigration(ctx, entries, userCatalogue, limits)
}
