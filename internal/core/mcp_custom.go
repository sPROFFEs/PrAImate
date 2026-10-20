package core

// Custom MCP servers — add a locally-run or self-hosted MCP that isn't
// in the built-in catalogue (e.g. hexstrike-ai). The user supplies the
// command (stdio) or URL (http/sse), plus optional args and env. This
// is a thin convenience over ConnectMCP that parses the simple text
// inputs a TUI/GUI form collects.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// AddCustomMCPRequest is the form-friendly shape for a user-defined MCP.
// Transport drives which of Command/URL is required.
type AddCustomMCPRequest struct {
	Name      string // required; also derives the id when ID is empty
	ID        string // optional; slugified from Name if blank
	Transport string // "stdio" | "http" | "sse"
	Command   string // stdio: the executable (may include args inline)
	Args      []string
	URL       string            // http/sse endpoint
	Env       map[string]string // KEY=VALUE pairs (e.g. API tokens)
	Auth      map[string]string // Legacy/OAuth metadata; preserved on edit.
	Headers   map[string]string // nil preserves headers; an empty map clears them.
	Enabled   *bool             // optional; defaults to enabled for new servers
}

// AddCustomMCP validates and persists a user-defined MCP server. Returns
// the stored server.
func (c *Core) AddCustomMCP(ctx context.Context, req AddCustomMCPRequest) (*MCPServer, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("AddCustomMCP: Name required")
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		id = mcpSlug(name)
	}
	if id == "" {
		return nil, fmt.Errorf("AddCustomMCP: could not derive an id from name %q", name)
	}
	transport := strings.TrimSpace(req.Transport)
	if transport == "" {
		transport = string(MCPTransportStdio)
	}

	// Allow the command field to carry inline args ("python server.py
	// --port 9000"); split them out when explicit Args weren't given.
	// Keep this shell-like enough for quoted paths, but still persist a
	// direct argv vector so the MCP client does not need a shell.
	command := strings.TrimSpace(req.Command)
	args := req.Args
	if transport == string(MCPTransportStdio) && len(args) == 0 && command != "" {
		fields, err := splitCommandLine(command)
		if err != nil {
			return nil, fmt.Errorf("AddCustomMCP: %w", err)
		}
		if len(fields) > 1 {
			command = fields[0]
			args = fields[1:]
		}
	}
	if transport == string(MCPTransportStdio) {
		command = expandMCPProcessArg(command)
		args = expandMCPProcessArgs(args)
	}

	auth := req.Auth
	if req.Headers != nil {
		if err := validateMCPHeaders(req.Headers); err != nil {
			return nil, err
		}
		auth = map[string]string{}
		for key, value := range req.Auth {
			auth[key] = value
		}
		delete(auth, "header")
		delete(auth, "token")
		delete(auth, "headers")
		if len(req.Headers) > 0 {
			body, _ := json.Marshal(req.Headers)
			auth["headers"] = string(body)
		}
	}
	return c.ConnectMCP(ctx, ConnectMCPRequest{
		ID:        id,
		Name:      name,
		Transport: MCPTransport(transport),
		Command:   command,
		Args:      args,
		URL:       strings.TrimSpace(req.URL),
		Env:       req.Env,
		Auth:      auth,
		Enabled:   req.Enabled,
	})
}

// UpdateMCPServer edits a stored server without changing its stable ID or
// enabled state. Editing a catalogue-derived entry turns it into a custom
// definition so future catalogue changes cannot overwrite the user's values.
func (c *Core) UpdateMCPServer(ctx context.Context, id string, req AddCustomMCPRequest) (*MCPServer, error) {
	current, err := c.GetMCPServer(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	req.ID = current.ID
	req.Enabled = &current.Enabled
	if req.Auth == nil {
		req.Auth = current.Auth
	}
	return c.AddCustomMCP(ctx, req)
}

// ParseEnvLines turns "KEY=VALUE" lines (newline- or comma-separated)
// into a map. Blank lines and lines without "=" are skipped. Shared by
// the TUI and GUI custom-MCP forms.
func ParseEnvLines(s string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	repl := strings.NewReplacer("\r", "\n", ",", "\n")
	for _, line := range strings.Split(repl.Replace(s), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if k != "" {
			out[k] = v
		}
	}
	return out
}

// mcpSlug normalises a display name to a stable id: lowercase, spaces
// and runs of punctuation to single hyphens, [a-z0-9-] only.
func mcpSlug(s string) string {
	return MCPSlug(s)
}

// MCPSlug standardizes an MCP server name or ID into a predictable
// kebab-case format. This helps resolve naming mismatches (e.g. "My Server"
// vs "my-server") between UI components and agent configurations.
func MCPSlug(s string) string {
	b := make([]byte, 0, len(s))
	prevDash := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b = append(b, c+('a'-'A'))
			prevDash = false
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b = append(b, c)
			prevDash = false
		default:
			if !prevDash {
				b = append(b, '-')
				prevDash = true
			}
		}
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	return string(b)
}

func splitCommandLine(s string) ([]string, error) {
	var fields []string
	var b strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	haveToken := false

	flush := func() {
		fields = append(fields, b.String())
		b.Reset()
		haveToken = false
	}

	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			haveToken = true
			continue
		}
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
				haveToken = true
			} else {
				b.WriteRune(r)
				haveToken = true
			}
		case inDouble:
			switch r {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
				haveToken = true
			default:
				b.WriteRune(r)
				haveToken = true
			}
		default:
			switch {
			case unicode.IsSpace(r):
				if haveToken {
					flush()
				}
			case r == '\\':
				escaped = true
				haveToken = true
			case r == '\'':
				inSingle = true
				haveToken = true
			case r == '"':
				inDouble = true
				haveToken = true
			default:
				b.WriteRune(r)
				haveToken = true
			}
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	if haveToken {
		flush()
	}
	return fields, nil
}

func expandMCPProcessArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = expandMCPProcessArg(arg)
	}
	return out
}

func expandMCPProcessArg(s string) string {
	if s == "" {
		return s
	}
	s = expandEnvPreserveUnset(s)
	if s == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
	}
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, s[2:])
		}
	}
	return s
}

func expandEnvPreserveUnset(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		if s[i+1] == '{' {
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteByte(s[i])
				i++
				continue
			}
			end += i + 2
			name := s[i+2 : end]
			if value, ok := os.LookupEnv(name); ok {
				b.WriteString(value)
			} else {
				b.WriteString(s[i : end+1])
			}
			i = end + 1
			continue
		}
		j := i + 1
		for j < len(s) && isEnvNameByte(s[j]) {
			j++
		}
		if j == i+1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		name := s[i+1 : j]
		if value, ok := os.LookupEnv(name); ok {
			b.WriteString(value)
		} else {
			b.WriteString(s[i:j])
		}
		i = j
	}
	return b.String()
}

func isEnvNameByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_'
}
