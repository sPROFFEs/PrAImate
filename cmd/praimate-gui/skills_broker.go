package main

// Skills broker — the loopback HTTP server that powers the internal
// MCP skills shim for terminal CLIs and child sessions.
//
// Serves on 127.0.0.1 (ephemeral dynamic port) with an in-memory token.
// Completely isolated from user-configured MCP servers.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

type skillsBroker struct {
	addr  string
	token string
	core  *core.Core
	mu    sync.Mutex
}

func newSkillsBroker(c *core.Core) (*skillsBroker, error) {
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	b := &skillsBroker{
		token: hex.EncodeToString(tok),
		core:  c,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b.addr = ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/skills/", b.handleSkills)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return b, nil
}

func (b *skillsBroker) handleSkills(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Praimate-Token") != b.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// URL shape: /skills/<chatID>/<action> where action is list, load, resource
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/skills/"), "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	chatID := parts[0]
	action := parts[1]

	ctx := r.Context()
	switch action {
	case "list":
		b.handleList(ctx, w, chatID)
	case "load":
		name := r.URL.Query().Get("name")
		b.handleLoad(ctx, w, chatID, name)
	case "resource":
		name := r.URL.Query().Get("name")
		relPath := r.URL.Query().Get("path")
		b.handleResource(ctx, w, chatID, name, relPath)
	default:
		http.Error(w, "unknown action", http.StatusNotFound)
	}
}

type skillListing struct {
	Name        string `json:"name"`
	Ref         string `json:"ref"`
	Description string `json:"description"`
}

func (b *skillsBroker) resolveChatSkillEntries(ctx context.Context, chatID string) ([]skills.SkillVersionLockEntry, error) {
	if b.core == nil {
		return nil, fmt.Errorf("core unavailable")
	}
	if strings.HasPrefix(chatID, "agent:") {
		agentID := strings.TrimPrefix(chatID, "agent:")
		agent, err := b.core.GetAgent(ctx, agentID)
		if err == nil && agent != nil && agent.SkillsLock != nil && len(agent.SkillsLock.Entries) > 0 {
			return agent.SkillsLock.Entries, nil
		}
	}
	if chatID != "" && !strings.HasPrefix(chatID, "agent:") {
		chat, err := b.core.GetChat(ctx, chatID)
		if err == nil && chat != nil {
			if chat.Settings.SkillsLock != nil && len(chat.Settings.SkillsLock.Entries) > 0 {
				return chat.Settings.SkillsLock.Entries, nil
			}
			if chat.AgentID != "" {
				agent, err := b.core.GetAgent(ctx, chat.AgentID)
				if err == nil && agent != nil && agent.SkillsLock != nil && len(agent.SkillsLock.Entries) > 0 {
					return agent.SkillsLock.Entries, nil
				}
			}
		}
	}
	// Fallback to defaults
	_, lock, err := b.core.SkillDefaultsV2(ctx)
	if err == nil && lock != nil {
		return lock.Entries, nil
	}
	return nil, nil
}

func (b *skillsBroker) handleList(ctx context.Context, w http.ResponseWriter, chatID string) {
	entries, err := b.resolveChatSkillEntries(ctx, chatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer host.Close()

	var list []skillListing
	for _, entry := range entries {
		_, files, err := host.ReadVersion(ctx, entry.Ref, entry.Digest)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Path == "SKILL.md" {
				if m, err := skills.ParsePackageManifest(f.Content); err == nil {
					name := m.Name
					if name == "" {
						name = entry.Ref
					}
					list = append(list, skillListing{
						Name:        name,
						Ref:         entry.Ref,
						Description: m.Description,
					})
				}
				break
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func (b *skillsBroker) handleLoad(ctx context.Context, w http.ResponseWriter, chatID, name string) {
	if strings.TrimSpace(name) == "" {
		http.Error(w, "missing skill name", http.StatusBadRequest)
		return
	}
	entries, err := b.resolveChatSkillEntries(ctx, chatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer host.Close()

	targetName := strings.ToLower(strings.TrimSpace(name))
	for _, entry := range entries {
		_, files, err := host.ReadVersion(ctx, entry.Ref, entry.Digest)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Path == "SKILL.md" {
				m, err := skills.ParsePackageManifest(f.Content)
				if err == nil {
					manifestName := strings.ToLower(m.Name)
					refName := strings.ToLower(entry.Ref)
					if manifestName == targetName || refName == targetName || strings.HasSuffix(refName, "/"+targetName) {
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						_, _ = w.Write([]byte(m.Body))
						return
					}
				}
				break
			}
		}
	}
	http.Error(w, fmt.Sprintf("skill %q not found in active session bindings", name), http.StatusNotFound)
}

func (b *skillsBroker) handleResource(ctx context.Context, w http.ResponseWriter, chatID, name, relPath string) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(relPath) == "" {
		http.Error(w, "missing name or path", http.StatusBadRequest)
		return
	}
	entries, err := b.resolveChatSkillEntries(ctx, chatID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer host.Close()

	targetName := strings.ToLower(strings.TrimSpace(name))
	cleanPath := strings.TrimPrefix(relPath, "/")
	for _, entry := range entries {
		_, files, err := host.ReadVersion(ctx, entry.Ref, entry.Digest)
		if err != nil {
			continue
		}
		isMatch := false
		for _, f := range files {
			if f.Path == "SKILL.md" {
				if m, err := skills.ParsePackageManifest(f.Content); err == nil {
					manifestName := strings.ToLower(m.Name)
					refName := strings.ToLower(entry.Ref)
					if manifestName == targetName || refName == targetName || strings.HasSuffix(refName, "/"+targetName) {
						isMatch = true
					}
				}
				break
			}
		}
		if isMatch {
			for _, f := range files {
				if f.Path == cleanPath {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = w.Write([]byte(f.Content))
					return
				}
			}
		}
	}
	http.Error(w, fmt.Sprintf("resource %q not found in skill %q", relPath, name), http.StatusNotFound)
}

type skillsProviderConfig struct {
	Command string
	Args    []string
}

func (a *App) ensureSkillsBroker() (*skillsBroker, error) {
	a.skillsBrokerMu.Lock()
	defer a.skillsBrokerMu.Unlock()
	if a.skillsBroker != nil {
		return a.skillsBroker, nil
	}
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	b, err := newSkillsBroker(c)
	if err != nil {
		return nil, err
	}
	a.skillsBroker = b
	return b, nil
}

func (a *App) skillsProvider(chatID string) *skillsProviderConfig {
	b, err := a.ensureSkillsBroker()
	if err != nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return &skillsProviderConfig{
		Command: exe,
		Args: []string{
			"-mcp-skills", "http://" + b.addr + "/skills/" + chatID,
			"-mcp-token", b.token,
		},
	}
}
