package main

// MCP skills shim — stdio MCP server for dynamic on-demand skill loading.
//
// Spawning contract:
//   praimate-gui -mcp-skills <url> -mcp-token <token>
//
// Tools exposed to CLIs (Claude, OpenClaude, OpenCode, Codex):
//   - list_available_skills
//   - load_skill (name)
//   - read_skill_resource (name, path)

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func runSkillsShim(stdin io.Reader, stdout io.Writer, endpoint, token string) int {
	client := &http.Client{Timeout: 30 * time.Second}

	fetch := func(path string, query map[string]string) (string, error) {
		u := strings.TrimRight(endpoint, "/") + path
		if len(query) > 0 {
			q := url.Values{}
			for k, v := range query {
				q.Set(k, v)
			}
			u += "?" + q.Encode()
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("X-Praimate-Token", token)
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("skills broker unreachable: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("skills broker error (%d): %s", resp.StatusCode, string(b))
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}

	return serveMCPSkills(stdin, stdout, fetch)
}

func serveMCPSkills(in io.Reader, out io.Writer, fetch func(path string, q map[string]string) (string, error)) int {
	enc := json.NewEncoder(out)
	respond := func(id json.RawMessage, result any) {
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	respondErr := func(id json.RawMessage, code int, msg string) {
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
	}

	br := bufio.NewReaderSize(in, 1024*1024)
	for {
		raw, err := br.ReadBytes('\n')
		line := bytes.TrimSpace(raw)
		if len(line) > 0 {
			var req mcpRequest
			if jerr := json.Unmarshal(line, &req); jerr == nil {
				switch {
				case req.Method == "initialize":
					respond(req.ID, map[string]any{
						"protocolVersion": "2024-11-05",
						"capabilities":    map[string]any{"tools": map[string]any{}},
						"serverInfo":      map[string]any{"name": "praimate-skills", "version": "1.0"},
					})
				case req.Method == "ping":
					respond(req.ID, map[string]any{})
				case req.Method == "tools/list":
					respond(req.ID, map[string]any{
						"tools": []any{
							map[string]any{
								"name":        "list_available_skills",
								"description": "Lists all procedural skills and workflows available in this PrAImate session.",
								"inputSchema": map[string]any{
									"type":       "object",
									"properties": map[string]any{},
								},
							},
							map[string]any{
								"name":        "load_skill",
								"description": "Loads the full procedural instructions, guidance, and rules for a specified skill by name.",
								"inputSchema": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name": map[string]any{"type": "string", "description": "The name or reference of the skill to load (e.g. 'forge-review')"},
									},
									"required": []string{"name"},
								},
							},
							map[string]any{
								"name":        "read_skill_resource",
								"description": "Reads a reference document, template, or auxiliary file bundled with a skill.",
								"inputSchema": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"name": map[string]any{"type": "string", "description": "The name of the skill"},
										"path": map[string]any{"type": "string", "description": "Relative path within the skill (e.g. 'references/checklist.md')"},
									},
									"required": []string{"name", "path"},
								},
							},
						},
					})
				case req.Method == "tools/call":
					name := req.Params.Name
					switch name {
					case "list_available_skills":
						res, err := fetch("/list", nil)
						if err != nil {
							respond(req.ID, map[string]any{
								"isError": true,
								"content": []any{map[string]any{"type": "text", "text": "Failed to list skills: " + err.Error()}},
							})
						} else {
							respond(req.ID, map[string]any{
								"content": []any{map[string]any{"type": "text", "text": res}},
							})
						}

					case "load_skill":
						skillName, _ := req.Params.Arguments["name"].(string)
						if strings.TrimSpace(skillName) == "" {
							respondErr(req.ID, -32602, "missing required argument: name")
							break
						}
						res, err := fetch("/load", map[string]string{"name": skillName})
						if err != nil {
							respond(req.ID, map[string]any{
								"isError": true,
								"content": []any{map[string]any{"type": "text", "text": "Failed to load skill: " + err.Error()}},
							})
						} else {
							respond(req.ID, map[string]any{
								"content": []any{map[string]any{"type": "text", "text": res}},
							})
						}

					case "read_skill_resource":
						skillName, _ := req.Params.Arguments["name"].(string)
						relPath, _ := req.Params.Arguments["path"].(string)
						if strings.TrimSpace(skillName) == "" || strings.TrimSpace(relPath) == "" {
							respondErr(req.ID, -32602, "missing required arguments: name, path")
							break
						}
						res, err := fetch("/resource", map[string]string{"name": skillName, "path": relPath})
						if err != nil {
							respond(req.ID, map[string]any{
								"isError": true,
								"content": []any{map[string]any{"type": "text", "text": "Failed to read resource: " + err.Error()}},
							})
						} else {
							respond(req.ID, map[string]any{
								"content": []any{map[string]any{"type": "text", "text": res}},
							})
						}

					default:
						respondErr(req.ID, -32602, "unknown tool: "+name)
					}

				case strings.HasPrefix(req.Method, "notifications/"):
					// fire-and-forget
				default:
					if len(req.ID) > 0 && string(req.ID) != "null" {
						respondErr(req.ID, -32601, "method not found: "+req.Method)
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return 0
			}
			fmt.Fprintln(out)
			return 1
		}
	}
}
