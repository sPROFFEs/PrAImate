package studio

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const extensionID = "PrAImate.praimate-studio"

// Desktop actions can use different Manager instances for the same profile.
var extensionSetupMu sync.Mutex

func prepareExtensions(ctx context.Context, bin string) error {
	extensionSetupMu.Lock()
	defer extensionSetupMu.Unlock()
	if err := provisionExtensions(); err != nil {
		return err
	}
	return ensureExtensionRegistered(ctx, bin)
}

func embeddedExtensionVersion() string {
	var manifest struct{ Version string }
	_ = json.Unmarshal([]byte(ExtensionPackageJSON), &manifest)
	return manifest.Version
}

// Code-OSS records the extensions visible to a profile in extensions.json.
// Copied files alone are not sufficient once that registry has been created.
// A missing registry is the editor's supported first-scan state.
func registeredExtension(root string) (string, bool, error) {
	legacy := filepath.Join(root, "praimate")
	body, err := os.ReadFile(filepath.Join(root, "extensions.json"))
	if errors.Is(err, os.ErrNotExist) {
		return legacy, true, nil
	}
	if err != nil {
		return legacy, false, fmt.Errorf("read Studio extension registry: %w", err)
	}
	var entries []struct {
		Identifier struct {
			ID string `json:"id"`
		} `json:"identifier"`
		Version          string `json:"version"`
		RelativeLocation string `json:"relativeLocation"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		return legacy, false, fmt.Errorf("read Studio extension registry: %w", err)
	}
	for _, entry := range entries {
		if !strings.EqualFold(entry.Identifier.ID, extensionID) {
			continue
		}
		if !filepath.IsLocal(entry.RelativeLocation) {
			return legacy, false, nil
		}
		dir := filepath.Join(root, entry.RelativeLocation)
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return legacy, false, nil
		}
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return legacy, false, err
		}
		rel, err := filepath.Rel(resolvedRoot, resolved)
		if err != nil || !filepath.IsLocal(rel) {
			return legacy, false, nil
		}
		return dir, entry.Version == embeddedExtensionVersion(), nil
	}
	return legacy, false, nil
}

func extensionArchive() (string, error) {
	f, err := os.CreateTemp("", "praimate-studio-*.vsix")
	if err != nil {
		return "", err
	}
	name := f.Name()
	w := zip.NewWriter(f)
	files := extensionFiles()
	archive := map[string]string{
		"[Content_Types].xml":    `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="json" ContentType="application/json"/><Default Extension="js" ContentType="application/javascript"/><Default Extension="png" ContentType="image/png"/><Default Extension="svg" ContentType="image/svg+xml"/><Default Extension="vsixmanifest" ContentType="text/xml"/></Types>`,
		"extension.vsixmanifest": fmt.Sprintf(`<?xml version="1.0"?><PackageManifest Version="2.0.0" xmlns="http://schemas.microsoft.com/developer/vsx-schema/2011"><Metadata><Identity Language="en-US" Id="praimate-studio" Version="%s" Publisher="PrAImate"/><DisplayName>PrAImate Studio</DisplayName><Description>PrAImate Studio integration</Description></Metadata><Installation><InstallationTarget Id="Microsoft.VisualStudio.Code"/></Installation><Dependencies/><Assets><Asset Type="Microsoft.VisualStudio.Code.Manifest" Path="extension/package.json" Addressable="true"/></Assets></PackageManifest>`, embeddedExtensionVersion()),
	}
	for path, body := range files {
		archive["extension/"+path] = body
	}
	for path, body := range archive {
		entry, createErr := w.Create(path)
		if createErr == nil {
			_, createErr = entry.Write([]byte(body))
		}
		if createErr != nil {
			w.Close()
			f.Close()
			os.Remove(name)
			return "", createErr
		}
	}
	if err := w.Close(); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func editorExtensionCommand(ctx context.Context, bin string, args ...string) (*exec.Cmd, error) {
	env := studioEnvironment(os.Environ(), map[string]string{"ELECTRON_RUN_AS_NODE": "", "VSCODE_IPC_HOOK_CLI": ""})
	script := filepath.Join(filepath.Dir(bin), "resources", "app", "out", "cli.js")
	if filepath.Base(bin) == "Electron" {
		script = filepath.Join(filepath.Dir(bin), "..", "Resources", "app", "out", "cli.js")
	}
	if info, err := os.Stat(script); err == nil && !info.IsDir() {
		args = append([]string{script}, args...)
		env = studioEnvironment(env, map[string]string{"ELECTRON_RUN_AS_NODE": "1"})
	} else if runtime.GOOS == "windows" || filepath.Base(bin) == "Electron" {
		return nil, fmt.Errorf("editor CLI is missing: %s", script)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	cmd.WaitDelay = 2 * time.Second
	hideEditorCLIConsole(cmd)
	return cmd, nil
}

func ensureExtensionRegistered(ctx context.Context, bin string) error {
	ext, err := ExtensionsDir()
	if err != nil {
		return err
	}
	_, registered, err := registeredExtension(ext)
	if err != nil {
		return err
	}
	if registered {
		return nil
	}
	data, err := UserDataDir()
	if err != nil {
		return err
	}
	archive, err := extensionArchive()
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd, err := editorExtensionCommand(ctx, bin, "--extensions-dir", ext, "--user-data-dir", data, "--install-extension", archive, "--force")
	if err != nil {
		return err
	}
	output := &editorStartupOutput{}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("register Studio extension: %w: %s", err, strings.TrimSpace(string(output.data)))
	}
	dir, registered, err := registeredExtension(ext)
	if err != nil {
		return err
	}
	if !registered || !extensionCurrent(dir) {
		return errors.New("editor did not register the bundled PrAImate extension; repair Studio and reopen its windows")
	}
	return nil
}
