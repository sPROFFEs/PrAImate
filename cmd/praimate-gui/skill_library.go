package main

import (
	"errors"
	"os"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

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
