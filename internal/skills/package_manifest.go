package skills

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// PackageManifest is portable descriptive metadata. In particular, AllowedTools
// is untrusted author intent and is never passed to a permissions broker.
// Unknown fields remain in the original PackageFile; parsing does not rewrite it.
type PackageManifest struct {
	Name          string
	Description   string
	License       string
	Compatibility string
	AllowedTools  string
	Metadata      map[string]string
	Body          string
}

const maxSkillMarkdown = 256 << 10

// ParsePackageManifest parses YAML using the repository's existing parser.
// Limits are enforced before decoding into values; aliases are unsupported in
// this initial profile, preventing recursive or amplified manifest expansion.
func ParsePackageManifest(raw []byte) (PackageManifest, error) {
	var out PackageManifest
	if len(raw) > maxSkillMarkdown {
		return out, errors.New("SKILL.md exceeds import limit")
	}
	if !utf8.Valid(raw) {
		return out, errors.New("SKILL.md is not UTF-8")
	}
	lines := bytes.SplitAfter(raw, []byte("\n"))
	if len(lines) < 3 || strings.TrimRight(string(lines[0]), "\r\n") != "---" {
		return out, errors.New("SKILL.md requires YAML frontmatter")
	}
	offset, end := len(lines[0]), -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(string(lines[i]), "\r\n") == "---" {
			end = offset
			offset += len(lines[i])
			break
		}
		offset += len(lines[i])
	}
	if end < 0 {
		return out, errors.New("unterminated SKILL.md frontmatter")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw[len(lines[0]):end], &doc); err != nil {
		return out, fmt.Errorf("invalid skill YAML: %w", err)
	}
	nodes := 0
	if err := validateManifestNode(&doc, 0, &nodes); err != nil {
		return out, err
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return out, errors.New("skill frontmatter must be a mapping")
	}
	var fields map[string]yaml.Node
	if err := doc.Content[0].Decode(&fields); err != nil {
		return out, err
	}
	for name, dst := range map[string]*string{"name": &out.Name, "description": &out.Description, "license": &out.License, "compatibility": &out.Compatibility, "allowed-tools": &out.AllowedTools} {
		if n, ok := fields[name]; ok {
			if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
				return out, fmt.Errorf("%s must be a string", name)
			}
			*dst = n.Value
		}
	}
	if n, ok := fields["metadata"]; ok {
		if n.Kind != yaml.MappingNode {
			return out, errors.New("metadata must be a string mapping")
		}
		out.Metadata = map[string]string{}
		for i := 0; i < len(n.Content); i += 2 {
			if n.Content[i].Tag != "!!str" || n.Content[i+1].Tag != "!!str" {
				return out, errors.New("metadata must contain strings")
			}
			out.Metadata[n.Content[i].Value] = n.Content[i+1].Value
		}
	}
	if len(out.Name) < 1 || len(out.Name) > 64 || strings.HasPrefix(out.Name, "-") || strings.HasSuffix(out.Name, "-") || strings.Contains(out.Name, "--") {
		return out, errors.New("invalid skill name")
	}
	for _, r := range out.Name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return out, errors.New("invalid skill name")
		}
	}
	if strings.TrimSpace(out.Description) == "" || utf8.RuneCountInString(out.Description) > 1024 {
		return out, errors.New("invalid skill description")
	}
	if utf8.RuneCountInString(out.Compatibility) > 500 {
		return out, errors.New("compatibility exceeds 500 characters")
	}
	out.Body = string(raw[offset:])
	return out, nil
}

func validateManifestNode(n *yaml.Node, depth int, count *int) error {
	*count++
	if depth > 16 || *count > 4096 {
		return errors.New("skill YAML complexity limit exceeded")
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("skill YAML aliases and anchors are unsupported")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
				return errors.New("invalid or duplicate YAML key")
			}
			seen[key.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := validateManifestNode(child, depth+1, count); err != nil {
			return err
		}
	}
	return nil
}
