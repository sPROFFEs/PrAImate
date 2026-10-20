package core

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestMCPHeadersAreLiteralAndSharedAcrossNativeAndCLIAdapters(t *testing.T) {
	want := map[string]string{"Authorization": "Basic fixture", "X-Key": "fixture-key", "X_key": "other-fixture"}
	body, _ := json.Marshal(want)
	server := MCPServer{ID: "fixture", Auth: map[string]string{"token": "legacy", "headers": string(body)}}
	if got := MCPHTTPHeaders(server); !reflect.DeepEqual(got, want) {
		t.Fatalf("headers differ: keys=%v", mapKeys(got))
	}
	req, _ := http.NewRequest(http.MethodGet, "http://fixture.invalid", nil)
	applyMCPHeaders(req, server, "")
	for key, value := range want {
		if req.Header.Get(key) != value {
			t.Fatalf("missing native header %s", key)
		}
	}
	for _, kind := range []string{"claude", "opencode", "codex"} {
		env := map[string]string{}
		var refs map[string]string
		if kind == "codex" {
			refs = codexHeaderEnvRefs(server, env)
		} else {
			refs = headerRefs(server, env, "Bearer ${%s}", "${%s}")
		}
		for key, value := range want {
			ref := refs[key]
			if kind != "codex" {
				ref = strings.TrimSuffix(strings.TrimPrefix(ref, "${"), "}")
			}
			if env[ref] != value {
				t.Fatalf("%s did not preserve header %s", kind, key)
			}
		}
	}
}

func TestMCPHeaderEditsPreserveLegacyAndCanExplicitlyClearCredentials(t *testing.T) {
	s := openTempStore(t)
	defer s.Close()
	c, _ := New(Options{Store: s})
	ctx := context.Background()
	req := AddCustomMCPRequest{Name: "Fixture", Transport: "http", URL: "https://fixture.invalid/mcp", Auth: map[string]string{"token": "fixture"}}
	server, err := c.AddCustomMCP(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.Auth = nil
	server, err = c.UpdateMCPServer(ctx, server.ID, req)
	if err != nil || MCPHTTPHeaders(*server)["Authorization"] != "Bearer fixture" {
		t.Fatal("legacy edit lost credentials", err)
	}
	req.Headers = map[string]string{"Authorization": "Basic fixture", "X-Api-Key": "fixture"}
	server, err = c.UpdateMCPServer(ctx, server.ID, req)
	if err != nil || MCPHTTPHeaders(*server)["Authorization"] != "Basic fixture" {
		t.Fatal("literal header edit failed", err)
	}
	req.Headers = map[string]string{}
	server, err = c.UpdateMCPServer(ctx, server.ID, req)
	if err != nil || len(MCPHTTPHeaders(*server)) != 0 {
		t.Fatal("explicit clear retained credentials", err)
	}
}

func TestMCPHeaderParserRejectsAmbiguityAndControlsWithoutLeakingValues(t *testing.T) {
	for _, text := range []string{"Authorization: fixture\nauthorization: fixture", "Bad Name: fixture", "X-Key: fixture\x00secret"} {
		_, err := ParseMCPHeaderLines(text)
		if err == nil || strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe parser result", err)
		}
	}
}
