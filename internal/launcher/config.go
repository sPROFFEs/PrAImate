// Package launcher holds the non-UI logic the PrAImate TUI calls into:
// user config, workspace discovery, agent CLI detection, and the
// compile-then-spawn sequence. The split exists so the logic is testable
// and reusable without dragging in Bubble Tea.
package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
)

// Config is the persisted, per-user launcher state.
type Config struct {
	// WorkspacesRoot is the absolute path of the directory that contains
	// one subdirectory per workspace.
	WorkspacesRoot string `json:"workspacesRoot"`
	// LastAgent is surfaced as the default selection in the agent picker.
	LastAgent string `json:"lastAgent,omitempty"`
	// WpcBinary, when set, overrides PATH lookup. Optional.
	WpcBinary string `json:"wpcBinary,omitempty"`

	// PrivacyNoticeAcceptedVersion records the disclosure revision the
	// user acknowledged. A versioned value lets a future material change
	// (telemetry, storage boundary, encryption model) show the notice once
	// again without prompting on every launch.
	PrivacyNoticeAcceptedVersion string `json:"privacyNoticeAcceptedVersion,omitempty"`

	// --- backup / git sync ---
	// BackupEnabled is the MASTER SWITCH for the cloud backup feature.
	// Off by default. When false, NOTHING backup-related runs: no
	// auto-sync hooks, no managed .gitignore / .gitattributes get
	// written, the Backup tab's other rows show as disabled until the
	// user flips it on. The workspaces root behaves exactly as it did
	// before 0.1.11 — just a directory of chats and templates.
	//
	// First-time setup explicitly chooses a new local history or an
	// existing remote before initialising the workspaces root. After
	// setup, this switch only pauses/resumes sync. Flipping it off removes
	// the configured remote and disables auto-sync but leaves the .git dir
	// and managed files alone so re-enabling later is cheap.
	BackupEnabled bool `json:"backupEnabled,omitempty"`
	// BackupRemoteURL is the configured remote when the workspaces root
	// is being managed as a git repo. Empty = no remote configured.
	BackupRemoteURL string `json:"backupRemoteUrl,omitempty"`
	// BackupAutoSync, when true, fires a sync on startup and on PrAImate
	// exit. Off by default. Sync is a fast-forward / commit-and-push
	// operation; divergence opens the resolution popup unless
	// BackupForceAlwaysLocal is also true.
	BackupAutoSync bool `json:"backupAutoSync,omitempty"`
	// BackupForceAlwaysLocal, when true, bypasses the divergence popup
	// in the auto-sync path and force-pushes local over remote. Loud
	// activation warning. Guarded against the "two machines, both
	// force-pushing" case by the Machine-ID check in the auto-sync
	// hook.
	BackupForceAlwaysLocal bool `json:"backupForceAlwaysLocal,omitempty"`
	// BackupLastSyncAt is the UTC timestamp of the last successful
	// sync (push, pull, or in-sync verification). Surfaced in the
	// Backup screen's header.
	BackupLastSyncAt time.Time `json:"backupLastSyncAt,omitempty"`
	// BackupMachineID is a per-machine random identifier embedded in
	// commit message trailers. Used by the auto-sync safeguard to
	// detect "another machine just pushed; refuse to clobber."
	// Generated lazily on first auto-sync activation.
	BackupMachineID string `json:"backupMachineId,omitempty"`

	// --- local LLM default (self-hosted endpoint reused across chats) ---
	// These are the GLOBAL default connection details for a local /
	// self-hosted OpenAI-compatible endpoint (Ollama, GPUStack, vLLM,
	// LiteLLM, …). Their only job is to spare the user retyping the
	// same endpoint URL + key + token caps on every new chat: when set,
	// the new-chat Ollama wizard offers "use the saved endpoint" as a
	// one-keystroke
	// alternative to typing a fresh one. They are NOT per-chat truth —
	// the chat's own chat.Settings.Ollama remains authoritative; the
	// wizard always proceeds to the live model query + per-agent picks
	// so each chat can diverge. Model and agent selection are
	// deliberately NOT stored here (backend defaults only by design).
	//
	// DefaultLocalEndpoint empty ⇒ no default configured; the wizard
	// behaves exactly as before (blank endpoint entry).
	DefaultLocalEndpoint string `json:"defaultLocalEndpoint,omitempty"`
	DefaultLocalAPIKey   string `json:"defaultLocalApiKey,omitempty"`
	// DefaultLocalWireAPI is "", "responses", or "chat" — carried so the
	// codex compat path can reuse the saved choice. Empty = unset/auto.
	DefaultLocalWireAPI string `json:"defaultLocalWireApi,omitempty"`
	// DefaultLocalContextTokens / DefaultLocalOutputTokens are copied into
	// each chat's Local endpoint wizard as model capability hints. The
	// chat-level OllamaSettings remains authoritative after apply.
	DefaultLocalContextTokens int `json:"defaultLocalContextTokens,omitempty"`
	DefaultLocalOutputTokens  int `json:"defaultLocalOutputTokens,omitempty"`
}

// HasLocalDefault reports whether a global default local-LLM endpoint is
// configured. The new-chat Ollama wizard uses this to decide whether to
// offer the "use saved endpoint" shortcut.
func (c *Config) HasLocalDefault() bool {
	return c != nil && c.DefaultLocalEndpoint != ""
}

// ConfigPaths returns the directory and file used for persistent config.
// On Linux this is $XDG_CONFIG_HOME/praimate/config.json (or ~/.config/...);
// on Windows it is %AppData%/praimate/... — courtesy of os.UserConfigDir().
func ConfigPaths() (dir, file string, err error) {
	dir, err = appdata.Root()
	if err != nil {
		return "", "", err
	}
	file = filepath.Join(dir, "config.json")
	return dir, file, nil
}

// LoadConfig returns (nil, nil) if the config file does not exist yet —
// callers treat that as the first-run signal.
func LoadConfig() (*Config, error) {
	_, file, err := ConfigPaths()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return &c, nil
}

// SaveConfig writes the config atomically (write to .tmp + rename) so a
// crash mid-write doesn't corrupt the file.
func SaveConfig(c *Config) error {
	dir, file, err := ConfigPaths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		return err
	}
	return os.Chmod(file, 0o600)
}
