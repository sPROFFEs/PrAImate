package main

// IDE-style file operations for the agent authoring studio. The agent's
// on-disk folder is its knowledge dir (<config>/praimate/agents/<id>/
// knowledge/) — documents the user manages plus the graphify-out RAG
// index. These bindings list the tree and read/write/create files inside
// it (path-safety guarded so nothing escapes the knowledge folder). The
// agent DEFINITION itself lives in the DB and is edited via AgentYAML /
// SaveAgentYAML; optional runtime capabilities live beside the knowledge
// folder in runtime.json and are validated independently.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// PreviewGuidedAgent deterministically expands the guided form without
// writing anything. The final wizard step renders this exact result.
func (a *App) PreviewGuidedAgent(req core.GuidedAgentRequest) (*core.GuidedAgentPreview, error) {
	if _, err := a.requireCore(); err != nil {
		return nil, err
	}
	return core.PreviewGuidedAgent(req)
}

func (a *App) CreateGuidedAgent(req core.GuidedAgentRequest) (*core.Agent, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.CreateGuidedAgent(a.ctx, req)
	if err != nil {
		return nil, err
	}
	if agent.Knowledge != "" {
		if err := a.EnableAgentKnowledge(agent.ID); err != nil {
			_ = c.DeleteAgent(a.ctx, agent.ID)
			if runtimePath, pathErr := core.AgentRuntimePath(agent.ID); pathErr == nil {
				_ = os.Remove(runtimePath)
			}
			return nil, fmt.Errorf("create knowledge folder: %w", err)
		}
	}
	return agent, nil
}

func (a *App) AgentRuntimeJSON(id string) (string, error) {
	if _, err := a.requireCore(); err != nil {
		return "", err
	}
	manifest, err := core.LoadAgentRuntime(id)
	if err != nil || manifest == nil {
		return "", err
	}
	raw, err := core.MarshalAgentRuntime(manifest)
	return string(raw), err
}

func (a *App) AgentRuntimeConfig(id string) (*core.EffectiveAgentConfig, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return nil, err
	}
	return c.ResolveEffectiveAgentConfig(a.ctx, agent)
}

func (a *App) EnableAgentRuntime(id string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	if _, err := c.GetAgent(a.ctx, id); err != nil {
		return "", err
	}
	manifest := &core.AgentRuntimeManifest{
		Schema: core.AgentRuntimeSchema, PresetOrigin: core.PresetCustom, Mode: core.RuntimeNative,
	}
	raw, err := core.MarshalAgentRuntime(manifest)
	if err != nil {
		return "", err
	}
	if err := core.SaveAgentRuntime(id, manifest); err != nil {
		return "", err
	}
	return string(raw), nil
}

func (a *App) SaveAgentRuntimeJSON(id, body string) (*core.EffectiveAgentConfig, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return nil, err
	}
	manifest, err := core.ParseAgentRuntime([]byte(body))
	if err != nil {
		return nil, err
	}
	if err := core.SaveAgentRuntime(id, manifest); err != nil {
		return nil, err
	}
	return c.ResolveEffectiveAgentConfig(a.ctx, agent)
}

// AgentFileNode is one entry in the agent's knowledge tree.
type AgentFileNode struct {
	Rel     string `json:"rel"`     // slash path relative to the knowledge dir
	Name    string `json:"name"`    // base name
	IsDir   bool   `json:"isDir"`   //
	IsIndex bool   `json:"isIndex"` // part of the graphify-out RAG index
	Depth   int    `json:"depth"`   // nesting depth (0 = top level)
}

// resolveKnowPath joins rel under the agent's knowledge dir and rejects
// anything that escapes it. Returns (knowledgeDir, absPath).
func resolveKnowPath(id, rel string) (string, string, error) {
	dir, err := core.AgentKnowledgeDir(id)
	if err != nil {
		return "", "", err
	}
	clean := strings.TrimPrefix(filepath.Clean("/"+filepath.ToSlash(rel)), "/")
	abs := filepath.Join(dir, filepath.FromSlash(clean))
	rp, err := filepath.Rel(dir, abs)
	if err != nil || rp == ".." || strings.HasPrefix(rp, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path %q escapes the knowledge folder", rel)
	}
	return dir, abs, nil
}

// AgentKnowledgeTree returns the agent's knowledge folder as a flat,
// sorted, depth-tagged list (dirs before files at each level). The
// graphify-out subtree is marked IsIndex so the UI can badge it.
func (a *App) AgentKnowledgeTree(id string) ([]AgentFileNode, error) {
	dir, err := core.AgentKnowledgeDir(id)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return []AgentFileNode{}, nil // no knowledge yet
	}
	var nodes []AgentFileNode
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil || p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		nodes = append(nodes, AgentFileNode{
			Rel:     rel,
			Name:    d.Name(),
			IsDir:   d.IsDir(),
			IsIndex: rel == "graphify-out" || strings.HasPrefix(rel, "graphify-out/") || rel == ".praimate-index" || strings.HasPrefix(rel, ".praimate-index/"),
			Depth:   strings.Count(rel, "/"),
		})
		return nil
	})
	// Stable order: each directory's children grouped, dirs first.
	sort.Slice(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		ai, bi := strings.LastIndex(a.Rel, "/"), strings.LastIndex(b.Rel, "/")
		ap, bp := a.Rel[:ai+1], b.Rel[:bi+1]
		if ap != bp {
			return ap < bp
		}
		if a.IsDir != b.IsDir {
			return a.IsDir // dirs first within a folder
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return nodes, nil
}

// AgentReadKnowledgeFile reads a text file from the knowledge folder.
func (a *App) AgentReadKnowledgeFile(id, rel string) (string, error) {
	_, abs, err := resolveKnowPath(id, rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if len(b) > 2<<20 {
		return "", fmt.Errorf("%s is too large to edit here (%d KB)", rel, len(b)/1024)
	}
	return string(b), nil
}

// AgentWriteKnowledgeFile writes (saves) a knowledge file.
func (a *App) AgentWriteKnowledgeFile(id, rel, content string) error {
	_, abs, err := resolveKnowPath(id, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// AgentRenameKnowledgeFile moves/renames a file inside the agent's
// knowledge folder. Both src and dst are slash-relative paths; dst is
// path-safety checked and must not escape the folder.
func (a *App) AgentRenameKnowledgeFile(id, src, dst string) (string, error) {
	src = strings.TrimSpace(src)
	dst = strings.TrimSpace(dst)
	if src == "" || dst == "" {
		return "", fmt.Errorf("source and destination names are required")
	}
	_, srcAbs, err := resolveKnowPath(id, src)
	if err != nil {
		return "", err
	}
	dir, dstAbs, err := resolveKnowPath(id, dst)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(srcAbs); err != nil {
		return "", fmt.Errorf("%s: %w", src, err)
	}
	if _, err := os.Stat(dstAbs); err == nil {
		return "", fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(srcAbs, dstAbs); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dir, dstAbs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// AgentCreateKnowledgeFile creates a new empty file (or a directory when
// rel ends in "/") in the knowledge folder and returns its rel path.
func (a *App) AgentCreateKnowledgeFile(id, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("a file name is required")
	}
	dir, abs, err := resolveKnowPath(id, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if strings.HasSuffix(rel, "/") {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", err
		}
	} else {
		if _, err := os.Stat(abs); err == nil {
			return "", fmt.Errorf("%s already exists", rel)
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(abs, []byte(""), 0o644); err != nil {
			return "", err
		}
	}
	return strings.TrimSuffix(filepath.ToSlash(rel), "/"), nil
}

var agentNameIDRE = regexp.MustCompile(`[^a-z0-9-]+`)

// CreateAgentFromName creates a new agent from the starter template with
// the chosen name (and a slug id) injected, persisting it so its folder /
// knowledge base exist immediately. Returns the saved agent.
func (a *App) CreateAgentFromName(name string) (*core.Agent, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("a name is required")
	}
	id := strings.Trim(agentNameIDRE.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if id == "" {
		return nil, fmt.Errorf("the name needs at least one letter or digit")
	}
	if _, err := c.GetAgent(a.ctx, id); err == nil {
		return nil, fmt.Errorf("an agent named %q (id %q) already exists", name, id)
	}
	agent, err := core.ParseAgentYAML(strings.NewReader(a.NewAgentTemplateYAML()))
	if err != nil {
		return nil, err
	}
	agent.ID = id
	agent.Name = name
	raw, err := core.MarshalAgentYAML(agent)
	if err != nil {
		return nil, err
	}
	saved, err := c.ImportAgentYAML(a.ctx, raw, "")
	if err == nil && saved != nil {
		_ = a.EnableAgentKnowledge(saved.ID)
	}
	return saved, err
}
