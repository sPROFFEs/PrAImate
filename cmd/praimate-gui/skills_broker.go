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
	"path/filepath"
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

func normalizeSkillIdentifier(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimSuffix(s, "/skill.md")
	s = strings.TrimSuffix(s, ".md")
	return s
}

func skillMatchesTarget(manifestName, ref, input string) bool {
	normInput := normalizeSkillIdentifier(input)
	if normInput == "" {
		return false
	}
	normManifest := normalizeSkillIdentifier(manifestName)
	normRef := normalizeSkillIdentifier(ref)

	if normManifest == normInput || normRef == normInput {
		return true
	}
	inputHyphen := strings.ReplaceAll(normInput, "_", "-")
	manifestHyphen := strings.ReplaceAll(normManifest, "_", "-")
	refHyphen := strings.ReplaceAll(normRef, "_", "-")
	if manifestHyphen == inputHyphen || refHyphen == inputHyphen {
		return true
	}

	inputBase := filepath.Base(inputHyphen)
	manifestBase := filepath.Base(manifestHyphen)
	refBase := filepath.Base(refHyphen)
	if manifestBase == inputBase || refBase == inputBase {
		return true
	}
	if strings.HasSuffix(refHyphen, "/"+inputBase) || strings.HasSuffix(inputHyphen, "/"+manifestBase) {
		return true
	}

	cleanInput := strings.TrimPrefix(strings.TrimPrefix(inputHyphen, "skills/own/"), "local/")
	cleanInput = strings.TrimPrefix(cleanInput, "own/")
	cleanRef := strings.TrimPrefix(refHyphen, "local/")
	if cleanInput == manifestHyphen || cleanInput == cleanRef {
		return true
	}
	return false
}

func resourceMatchesPath(filePath, targetSkillName, inputPath string) bool {
	fClean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(filePath)))
	pClean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(inputPath)))
	pClean = strings.TrimPrefix(pClean, "./")
	pClean = strings.TrimPrefix(pClean, "/")

	if strings.EqualFold(fClean, pClean) {
		return true
	}
	if strings.HasSuffix(strings.ToLower(pClean), "/"+strings.ToLower(fClean)) {
		return true
	}
	if targetSkillName != "" {
		sName := strings.ToLower(filepath.Base(normalizeSkillIdentifier(targetSkillName)))
		trimmed := strings.TrimPrefix(strings.ToLower(pClean), sName+"/")
		if strings.EqualFold(fClean, trimmed) {
			return true
		}
		trimmed = strings.TrimPrefix(strings.ToLower(pClean), "skills/own/"+sName+"/")
		if strings.EqualFold(fClean, trimmed) {
			return true
		}
	}
	return false
}

func (b *skillsBroker) resolveChatSkillEntries(ctx context.Context, chatID string) ([]skills.SkillVersionLockEntry, error) {
	if b.core == nil {
		return nil, fmt.Errorf("core unavailable")
	}
	seen := map[string]bool{}
	var entries []skills.SkillVersionLockEntry
	addEntries := func(list []skills.SkillVersionLockEntry) {
		for _, e := range list {
			key := e.Ref + "@" + e.Digest
			if !seen[key] {
				seen[key] = true
				entries = append(entries, e)
			}
		}
	}

	if strings.HasPrefix(chatID, "agent:") {
		agentID := strings.TrimPrefix(chatID, "agent:")
		agent, err := b.core.GetAgent(ctx, agentID)
		if err == nil && agent != nil {
			if agent.SkillsLock != nil {
				addEntries(agent.SkillsLock.Entries)
			}
			for _, w := range agent.Workflows {
				if w.SkillsLock != nil {
					addEntries(w.SkillsLock.Entries)
				}
			}
		}
	} else if chatID != "" {
		chat, err := b.core.GetChat(ctx, chatID)
		if err == nil && chat != nil {
			if chat.Settings.SkillsLock != nil {
				addEntries(chat.Settings.SkillsLock.Entries)
			}
			if chat.AgentID != "" {
				agent, err := b.core.GetAgent(ctx, chat.AgentID)
				if err == nil && agent != nil {
					if agent.SkillsLock != nil {
						addEntries(agent.SkillsLock.Entries)
					}
					for _, w := range agent.Workflows {
						if w.SkillsLock != nil {
							addEntries(w.SkillsLock.Entries)
						}
					}
				}
			}
		}
	}

	if len(entries) == 0 {
		_, lock, err := b.core.SkillDefaultsV2(ctx)
		if err == nil && lock != nil {
			addEntries(lock.Entries)
		}
	}
	return entries, nil
}

func (b *skillsBroker) handleList(ctx context.Context, w http.ResponseWriter, chatID string) {
	entries, _ := b.resolveChatSkillEntries(ctx, chatID)
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer host.Close()

	var list []skillListing
	seenRefs := map[string]bool{}

	addFromFiles := func(ref string, files []skills.PackageFile) {
		if seenRefs[ref] {
			return
		}
		for _, f := range files {
			if f.Path == "SKILL.md" {
				if m, err := skills.ParsePackageManifest(f.Content); err == nil {
					name := m.Name
					if name == "" {
						name = ref
					}
					seenRefs[ref] = true
					list = append(list, skillListing{
						Name:        name,
						Ref:         ref,
						Description: m.Description,
					})
				}
				break
			}
		}
	}

	for _, entry := range entries {
		_, files, err := host.ReadVersion(ctx, entry.Ref, entry.Digest)
		if err == nil {
			addFromFiles(entry.Ref, files)
		}
	}

	// Also list other approved versions available in the store
	if summaries, err := core.InstalledSkillSummaries(ctx); err == nil {
		for _, s := range summaries {
			if !seenRefs[s.Ref] && s.Approved {
				seenRefs[s.Ref] = true
				name := s.Name
				if name == "" {
					name = s.Ref
				}
				list = append(list, skillListing{
					Name:        name,
					Ref:         s.Ref,
					Description: s.Description,
				})
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
	entries, _ := b.resolveChatSkillEntries(ctx, chatID)
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer host.Close()

	checkVersion := func(ref, digest string) (string, bool) {
		_, files, err := host.ReadVersion(ctx, ref, digest)
		if err != nil {
			return "", false
		}
		for _, f := range files {
			if f.Path == "SKILL.md" {
				m, err := skills.ParsePackageManifest(f.Content)
				if err == nil && skillMatchesTarget(m.Name, ref, name) {
					return m.Body, true
				}
				break
			}
		}
		return "", false
	}

	// 1. Try session entries
	for _, entry := range entries {
		if body, ok := checkVersion(entry.Ref, entry.Digest); ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(body))
			return
		}
	}

	// 2. Fallback to all approved versions in the host store
	if summaries, err := core.InstalledSkillSummaries(ctx); err == nil {
		for _, s := range summaries {
			if s.Approved {
				if body, ok := checkVersion(s.Ref, s.Digest); ok {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = w.Write([]byte(body))
					return
				}
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
	entries, _ := b.resolveChatSkillEntries(ctx, chatID)
	host, err := core.OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer host.Close()

	checkResource := func(ref, digest string) ([]byte, bool) {
		_, files, err := host.ReadVersion(ctx, ref, digest)
		if err != nil {
			return nil, false
		}
		isMatch := false
		for _, f := range files {
			if f.Path == "SKILL.md" {
				if m, err := skills.ParsePackageManifest(f.Content); err == nil {
					if skillMatchesTarget(m.Name, ref, name) {
						isMatch = true
					}
				}
				break
			}
		}
		if isMatch {
			for _, f := range files {
				if resourceMatchesPath(f.Path, name, relPath) {
					return f.Content, true
				}
			}
		}
		return nil, false
	}

	// 1. Try session entries
	for _, entry := range entries {
		if content, ok := checkResource(entry.Ref, entry.Digest); ok {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write(content)
			return
		}
	}

	// 2. Fallback to all approved versions in host store
	if summaries, err := core.InstalledSkillSummaries(ctx); err == nil {
		for _, s := range summaries {
			if s.Approved {
				if content, ok := checkResource(s.Ref, s.Digest); ok {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = w.Write(content)
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
