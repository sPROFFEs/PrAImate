package core

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ParseAgentMarkdown accepts plain instructions and OpenCode/Claude-style YAML
// frontmatter. Native fields travel in a namespaced block for lossless exchange.
// Foreign model/tool/permission settings remain inert metadata in PrAImate.
func ParseAgentMarkdown(r io.Reader, filename string) (*Agent, error) {
	body, err := io.ReadAll(io.LimitReader(r, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 4<<20 || !utf8.Valid(body) {
		return nil, fmt.Errorf("agent Markdown must be UTF-8 and at most 4 MiB")
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(body), "\ufeff"), "\r\n", "\n")
	meta := map[string]any{}
	if strings.HasPrefix(text, "---\n") {
		lines := strings.Split(text, "\n")
		end := -1
		for i := 1; i < len(lines); i++ {
			if strings.TrimRight(lines[i], " \t") == "---" {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, fmt.Errorf("agent Markdown has unclosed YAML frontmatter")
		}
		d := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:end], "\n")))
		var front yaml.Node
		if err := d.Decode(&front); err != nil && err != io.EOF {
			return nil, fmt.Errorf("agent frontmatter: %w", err)
		}
		if len(front.Content) > 0 {
			preserveFrontmatterScalars(&front, map[*yaml.Node]bool{})
			if err := front.Decode(&meta); err != nil {
				return nil, fmt.Errorf("agent frontmatter: %w", err)
			}
		}
		var extra any
		if err := d.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("frontmatter must contain one YAML mapping")
		}
		text = strings.Join(lines[end+1:], "\n")
	}
	prompt := strings.TrimSpace(text)
	if prompt == "" {
		return nil, fmt.Errorf("agent Markdown requires instructions in its body")
	}
	var a *Agent
	nativeBlock := false
	if native, ok := meta["praimate"].(map[string]any); ok && strings.HasPrefix(fmt.Sprint(native["schema"]), "praimate.agent/") {
		nativeBlock = true
		raw, err := yaml.Marshal(native)
		if err != nil {
			return nil, err
		}
		// The Markdown body is authoritative when an exported document is edited.
		var fields map[string]any
		if err = yaml.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("invalid praimate frontmatter block")
		}
		fields["instructions"] = prompt
		raw, err = yaml.Marshal(fields)
		if err != nil {
			return nil, err
		}
		a, err = ParseAgentYAML(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		delete(meta, "praimate")
	} else {
		name := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
		if v, ok := meta["name"].(string); ok && strings.TrimSpace(v) != "" {
			name = v
		}
		id := sanitizeAgentID(name)
		if id == "" {
			return nil, fmt.Errorf("agent filename or name must produce a portable agent ID")
		}
		description, _ := meta["description"].(string)
		a = &Agent{Schema: AgentSchema, ID: id, Name: name, Description: description, Instructions: prompt,
			Supports: []string{"opencode", "praimate-code", "claude", "codex", "openclaude", "praimate-cli", "copilot", "antigravity"}}
	}
	if v, ok := meta["description"].(string); ok && !(nativeBlock && a.Description == "" && v == a.Name) {
		a.Description = v
	}
	// Public frontmatter is authoritative: removed options must not be
	// resurrected from an older native metadata snapshot.
	previous := a.Foreign
	a.Foreign = map[string]any{}
	if nativeBlock {
		if v, ok := previous["praimate"]; ok {
			a.Foreign["praimate"] = v
		}
	}
	for k, v := range meta {
		_, original := previous[k]
		if nativeBlock && !original && (k == "mode" && v == "all" || k == "description" && (v == a.Description || a.Description == "" && v == a.Name)) {
			continue
		}
		a.Foreign[k] = v
	}
	a.SourcePath = filename
	return a, a.Validate()
}

// MarshalAgentMarkdown writes a document usable as an OpenCode agent. The
// praimate block retains workflows, skills and knowledge metadata on reimport;
// standalone Markdown never includes the knowledge files or runtime manifest.
func MarshalAgentMarkdown(a *Agent) ([]byte, error) {
	raw, err := MarshalAgentYAML(a)
	if err != nil {
		return nil, err
	}
	var native map[string]any
	if err = yaml.Unmarshal(raw, &native); err != nil {
		return nil, err
	}
	delete(native, "instructions")
	meta := map[string]any{}
	for k, v := range a.Foreign {
		if k != "praimate" {
			meta[k] = v
		}
	}
	meta["description"] = a.Description
	if a.Description == "" {
		meta["description"] = a.Name
	}
	if _, ok := meta["mode"]; !ok {
		meta["mode"] = "all"
	}
	meta["praimate"] = native
	front, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return []byte("---\n" + string(front) + "---\n\n" + strings.TrimSpace(a.Instructions) + "\n"), nil
}

// Untyped YAML timestamps otherwise become time.Time and change spelling/type
// when metadata travels through JSON storage. Preserve the original scalar.
func preserveFrontmatterScalars(node *yaml.Node, seen map[*yaml.Node]bool) {
	if node == nil || seen[node] {
		return
	}
	seen[node] = true
	if node.Tag == "!!timestamp" {
		node.Tag = "!!str"
	}
	for _, child := range node.Content {
		preserveFrontmatterScalars(child, seen)
	}
	preserveFrontmatterScalars(node.Alias, seen)
}
