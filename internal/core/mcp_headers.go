package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

func validateMCPHeaders(headers map[string]string) error {
	seen := map[string]bool{}
	for key, value := range headers {
		if key == "" {
			return fmt.Errorf("HTTP header name is required")
		}
		for _, c := range key {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return fmt.Errorf("invalid HTTP header name")
			}
		}
		canonical := strings.ToLower(key)
		if seen[canonical] {
			return fmt.Errorf("duplicate HTTP header name")
		}
		seen[canonical] = true
		for _, c := range value {
			if c == 127 || (c < 32 && c != '\t') {
				return fmt.Errorf("HTTP header values cannot contain control characters")
			}
		}
	}
	return nil
}

// ParseMCPHeaderLines accepts one Header: value per line, without ever
// including credential values in parsing errors.
func ParseMCPHeaderLines(text string) (map[string]string, error) {
	out := map[string]string{}
	for n, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("HTTP headers line %d must use Header: value", n+1)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if err := validateMCPHeaders(map[string]string{key: value}); err != nil {
			return nil, fmt.Errorf("HTTP headers line %d: %w", n+1, err)
		}
		canonical := http.CanonicalHeaderKey(key)
		if _, exists := out[canonical]; exists {
			return nil, fmt.Errorf("duplicate HTTP header on line %d", n+1)
		}
		out[canonical] = value
	}
	return out, nil
}

func explicitMCPHeaders(server MCPServer) map[string]string {
	var headers map[string]string
	_ = json.Unmarshal([]byte(server.Auth["headers"]), &headers)
	return headers
}

// MCPHTTPHeaders is shared by native MCP, connection testing and CLI adapters.
// Explicit header values are literal; legacy token metadata keeps Bearer rules.
func MCPHTTPHeaders(server MCPServer) map[string]string {
	headers := map[string]string{}
	if key, value, ok := headerAndToken(server); ok {
		if strings.EqualFold(key, "Authorization") && !strings.HasPrefix(strings.ToLower(value), "bearer ") {
			value = "Bearer " + value
		}
		headers[http.CanonicalHeaderKey(key)] = value
	}
	for key, value := range explicitMCPHeaders(server) {
		headers[http.CanonicalHeaderKey(key)] = value
	}
	return headers
}

func explicitHeaderRefs(server MCPServer, env, refs map[string]string, pattern string) map[string]string {
	headers := explicitMCPHeaders(server)
	for _, header := range mapKeys(headers) {
		value := headers[header]
		name := mcpSecretEnvName(server.ID, header)
		for n := 2; ; n++ {
			if _, exists := env[name]; !exists {
				break
			}
			name = fmt.Sprintf("%s_%d", mcpSecretEnvName(server.ID, header), n)
		}
		env[name] = value
		if refs == nil {
			refs = map[string]string{}
		}
		for existing := range refs {
			if strings.EqualFold(existing, header) {
				delete(refs, existing)
			}
		}
		refs[header] = fmt.Sprintf(pattern, name)
	}
	return refs
}
