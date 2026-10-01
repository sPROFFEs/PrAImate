package main

import (
	"context"
	"errors"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/skills"
)

// The host constructs revision/review fields. Model arguments can never set
// approval, overwrite a pinned version, or supply a host transaction record.
func (a *App) assistantSkillMutation(ctx context.Context, in core.SkillLibraryRequest) (core.SkillLibraryResult, error) {
	listed, err := a.core.SkillLibrary(ctx, core.SkillLibraryRequest{Action: "list"})
	if err != nil {
		return core.SkillLibraryResult{}, err
	}
	in.Revision = listed.View.Revision
	return a.core.SkillLibrary(ctx, in)
}
func (a *App) assistantSkillDraft(ctx context.Context, ref, digest, newRef, content string) (any, error) {
	var in core.SkillLibraryRequest
	if ref != "" {
		in = core.SkillLibraryRequest{Action: "fork", Ref: ref, Digest: digest, NewRef: newRef}
	} else {
		in = core.SkillLibraryRequest{Action: "draft-create", NewRef: newRef, Files: []skills.PackageFile{{Path: "SKILL.md", Content: []byte(content)}}}
	}
	created, err := a.assistantSkillMutation(ctx, in)
	if err != nil {
		return nil, err
	}
	if ref != "" && content != "" {
		draft, err := a.core.SkillLibrary(ctx, core.SkillLibraryRequest{Action: "draft-read", Key: created.Key})
		if err != nil {
			return nil, err
		}
		found := false
		for i := range draft.Files {
			if draft.Files[i].Path == "SKILL.md" {
				draft.Files[i].Content = []byte(content)
				found = true
			}
		}
		if !found {
			return nil, errors.New("source skill has no SKILL.md")
		}
		if _, err := a.assistantSkillMutation(ctx, core.SkillLibraryRequest{Action: "draft-save", Key: created.Key, DraftRevision: draft.DraftRevision, Files: draft.Files}); err != nil {
			return nil, err
		}
	}
	preview, err := a.core.SkillLibrary(ctx, core.SkillLibraryRequest{Action: "draft-preview", Key: created.Key})
	if err != nil {
		return nil, err
	}
	return map[string]any{"draft_key": created.Key, "digest": preview.Review, "status": "draft", "hint": "Review this draft in Skills before publication. Existing versions, approvals and selections are preserved."}, nil
}
func (a *App) assistantSkillInstall(ctx context.Context, source, kind, gitRef, subpath, expectedReview string) (any, error) {
	in := core.SkillLibraryRequest{Action: "inspect", Source: source, Kind: kind, GitRef: gitRef, Subpath: subpath}
	preview, err := a.core.SkillLibrary(ctx, in)
	if err != nil {
		return nil, err
	}
	if expectedReview == "" {
		packages := []map[string]any{}
		for _, p := range preview.Packages {
			packages = append(packages, map[string]any{"index": p.Index, "ref": p.Ref, "digest": p.Digest, "name": p.Manifest.Name, "subpath": p.Subpath})
		}
		return map[string]any{"review": preview.Review, "revision": preview.GitRef, "packages": packages, "hint": "Inspect package content in Skills. Repeat install with this exact review digest; installation does not approve or activate it."}, nil
	}
	if preview.Review != expectedReview {
		return nil, errors.New("source changed; inspect and review again")
	}
	for _, p := range preview.Packages {
		in.Selections = append(in.Selections, core.SkillLibrarySelection{Index: p.Index, Ref: p.Ref})
	}
	in.Action = "install"
	in.Review = expectedReview
	if kind == "github" {
		in.GitRef = preview.GitRef
	}
	result, err := a.assistantSkillMutation(ctx, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"installed": len(result.Packages), "approved": false, "hint": "Review and approve exact installed content in Skills before selecting it for any agent or chat."}, nil
}
