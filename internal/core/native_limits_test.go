package core

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNativeLoadedContextDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, model, path, body string
		want                    int
	}{
		{"ollama", "qwen3", "/api/ps", `{"models":[{"name":"other","context_length":65536},{"name":"qwen3:latest","context_length":4096}]}`, 4096},
		{"studio", "local", "/api/v1/models", `{"models":[{"key":"model-file","max_context_length":131072,"loaded_instances":[{"id":"local","config":{"context_length":16384}}]}]}`, 16384},
		{"studio-smallest-instance", "file", "/api/v1/models", `{"models":[{"key":"file","loaded_instances":[{"id":"a","config":{"context_length":8192}},{"id":"b","config":{"context_length":4096}}]}]}`, 4096},
		{"studio-unloaded", "file", "/api/v1/models", `{"models":[{"key":"file","max_context_length":131072,"loaded_instances":[]}]}`, 0},
		{"llama", "local", "/props", `{"default_generation_settings":{"n_ctx":32768}}`, 32768},
		{"vllm", "local", "/v1/models", `{"data":[{"id":"other","max_model_len":4096},{"id":"local","max_model_len":65536}]}`, 65536},
		{"invalid", "local", "/props", `{"default_generation_settings":{"n_ctx":-1}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := nativeProvider{route: ChatLocalEndpoint{Endpoint: "http://local.test/proxy/v1", Model: tc.model, APIKey: "test-only"}, http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer test-only" || !strings.HasPrefix(r.URL.Path, "/proxy/") {
					t.Fatalf("unexpected metadata request: %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Path == "/proxy"+tc.path {
					return nativeHTTP(tc.body), nil
				}
				res := nativeHTTP(`{}`)
				res.StatusCode = 404
				return res, nil
			})}}
			window, source := p.discoverContext(context.Background())
			if window != tc.want || (window > 0 && source == "") {
				t.Fatalf("window=%d source=%s", window, source)
			}
		})
	}
}

func TestNativeLimitCacheAndExplicitOverrides(t *testing.T) {
	c := nativeTestCore(t)
	requests := 0
	c.nativeLimits.http = &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return nativeHTTP(`{"models":[{"name":"qwen3","context_length":4096}]}`), nil
	})}
	ctx := context.Background()
	for range 2 {
		route, err := c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "qwen3"}, "")
		if err != nil || route.ContextTokens != 4096 {
			t.Fatalf("route=%+v err=%v", route, err)
		}
	}
	if requests != 1 {
		t.Fatalf("metadata was not cached: %d requests", requests)
	}
	_, err := c.SaveLocalHost(ctx, LocalHost{ID: "gpu", Name: "GPU", Endpoint: "http://local.test/v1", ContextTokens: 32768})
	if err != nil {
		t.Fatal(err)
	}
	route, err := c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "qwen3", ContextTokens: 8192}, "")
	if err != nil || route.ContextTokens != 8192 || route.ContextSource != "chat override" {
		t.Fatalf("explicit 8192 overridden: %+v %v", route, err)
	}
	route, err = c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "qwen3"}, "")
	if err != nil || route.ContextTokens != 4096 || requests != 1 {
		t.Fatalf("loaded backend window did not supersede the host hint: %+v %v", route, err)
	}
}

func TestNativeContextDoesNotGuessFromModelName(t *testing.T) {
	if autoContextWindow("qwen3-128k", 0) != 8192 || autoContextWindow("qwen3", 4096) != 4096 {
		t.Fatal("model training limit overrode loaded context configuration")
	}
}

func TestNativeCompactionPreservesCurrentRequestAndInstructions(t *testing.T) {
	for _, largeSystem := range []bool{false, true} {
		messages := []nativeMessage{{Role: "system", Content: "mandatory rules"}, {Role: "user", Content: "exact request"}}
		idx := 1
		if largeSystem {
			idx = 0
		}
		original := strings.Repeat("important requirement ", 1000)
		messages[idx].Content = original
		budget := 2200
		if largeSystem {
			budget = 4000
		}
		_, _, err := compactNativeContext(messages, budget, nativeMessageTokens)
		if err == nil || messages[idx].Content != original {
			t.Fatal("oversized mandatory input was silently truncated")
		}
	}
}

func TestNativeWorkerRejectsOversizedInputBeforeTransport(t *testing.T) {
	_, err := ExecuteNativeWorker(context.Background(), ChatLocalEndpoint{Endpoint: "http://must-not-contact.test/v1", Model: "small", ContextTokens: 2048, OutputTokens: 1024}, "rules", strings.Repeat("text ", 2000))
	if err == nil || !strings.Contains(err.Error(), "worker context budget exceeded") {
		t.Fatalf("unbudgeted worker request: %v", err)
	}
}
