package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// OpenSkillInStudio extracts the skill files into an editable workspace folder and opens Studio.
func (a *App) OpenSkillInStudio(ref, digest string) (string, error) {
	if a.detachedClient != nil {
		return "", errors.New("open from main window")
	}
	if _, err := a.requireCore(); err != nil {
		return "", err
	}
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return "", err
	}
	defer host.Close()

	v, files, err := host.ReadVersion(a.ctx, ref, digest)
	if err != nil {
		return "", err
	}
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	folderName := strings.ReplaceAll(strings.ReplaceAll(v.Ref, "/", "_"), ":", "_")
	workDir := filepath.Join(root, "skills-workspace", folderName)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", err
	}
	for _, f := range files {
		targetPath := filepath.Join(workDir, f.Path)
		_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
		_ = os.WriteFile(targetPath, []byte(f.Content), 0644)
	}

	// Register the draft in the host skill store so it immediately appears in the Authoring drafts list.
	view, viewErr := host.View(a.ctx)
	if viewErr == nil {
		_, _ = host.Update(a.ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
			draft, err := skills.NewSkillDraft("own:"+folderName, v.Ref, files, skills.PackageLimits{})
			if err != nil {
				return err
			}
			return tx.SaveDraft(a.ctx, folderName, draft)
		})
	}

	return a.OpenEditorWindow(workDir, "", "claude", "", "", "", "", "")
}

// CreateSkillInStudio creates a fresh skill template in skills-workspace and opens Studio.
func (a *App) CreateSkillInStudio(name string) (string, error) {
	if a.detachedClient != nil {
		return "", errors.New("open from main window")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "my-new-skill"
	}
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	cleanName := strings.ReplaceAll(strings.ReplaceAll(name, "/", "_"), ":", "_")
	workDir := filepath.Join(root, "skills-workspace", cleanName)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", err
	}
	skillMD := filepath.Join(workDir, "SKILL.md")
	template := fmt.Sprintf("---\nname: %s\ndescription: Describe what this skill does and when to use it.\n---\n\n# %s\n\nWrite your skill instructions and procedures here.\n", name, name)
	if _, err := os.Stat(skillMD); os.IsNotExist(err) {
		_ = os.WriteFile(skillMD, []byte(template), 0644)
	}

	// Also register a draft in the host skill store so it appears in Authoring
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err == nil {
		defer host.Close()
		view, viewErr := host.View(a.ctx)
		if viewErr == nil {
			_, _ = host.Update(a.ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
				files := []skills.PackageFile{{Path: "SKILL.md", Content: []byte(template)}}
				refName := name
				if !strings.HasPrefix(refName, "local/") {
					refName = "local/" + refName
				}
				draft, err := skills.NewSkillDraft("own:"+cleanName, refName, files, skills.PackageLimits{})
				if err != nil {
					return err
				}
				return tx.SaveDraft(a.ctx, cleanName, draft)
			})
		}
	}

	return a.OpenEditorWindow(workDir, "", "claude", "", "", "", "", "")
}

func (a *App) ExportSkillPackageV2(ref, digest string) (string, error) {
	if a.detachedClient != nil {
		return "", errors.New("export from the main window")
	}
	if _, err := a.requireCore(); err != nil {
		return "", err
	}
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return "", err
	}
	defer host.Close()
	body, err := host.ExportPackageZIP(a.ctx, ref, digest)
	if err != nil {
		return "", err
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{Title: "Export reviewed skill package (no approvals)", DefaultFilename: "skill.zip", Filters: []wruntime.FileFilter{{DisplayName: "Skill ZIP", Pattern: "*.zip"}}})
	if err != nil || path == "" {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", errors.New("choose a new export filename; existing files are not overwritten")
	}
	_, writeErr := f.Write(body)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return path, nil
}

// PickSkillSourceV2 provides native pickers for local sources. Remote Git
// repositories stay as explicit text so the user can review the exact URL and
// revision before PrAImate performs network I/O.
func (a *App) PickSkillSourceV2(kind string) (string, error) {
	if a.detachedClient != nil {
		return "", errors.New("import skills from the main window")
	}
	switch kind {
	case "directory":
		return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{Title: "Select skill folder"})
	case "zip":
		return wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{Title: "Select skill ZIP", Filters: []wruntime.FileFilter{{DisplayName: "ZIP archives", Pattern: "*.zip"}}})
	default:
		return "", errors.New("a picker is only available for folders and ZIP files")
	}
}

// Host-only: never forwarded by detached RPC or exposed as a model tool.
func (a *App) SkillLibraryV2(body string) (core.SkillLibraryResult, error) {
	if a.detachedClient != nil {
		return core.SkillLibraryResult{}, errors.New("manage the skill library in the main window")
	}
	c, err := a.requireCore()
	if err != nil {
		return core.SkillLibraryResult{}, err
	}
	if len(body) > 12<<20 {
		return core.SkillLibraryResult{}, errors.New("skill editor request size limit exceeded")
	}
	var request core.SkillLibraryRequest
	if err := skills.DecodePortableSkillRecord([]byte(body), &request); err != nil {
		return core.SkillLibraryResult{}, err
	}
	return c.SkillLibrary(a.ctx, request)
}
