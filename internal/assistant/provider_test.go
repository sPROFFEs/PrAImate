package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPStructuredFallbackAndUsage(t *testing.T) {
	calls := 0
	input, output := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("incorrect request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		calls++
		if calls == 1 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"response_format unsupported"}`))
			return
		}
		if _, present := body["response_format"]; present {
			t.Error("unsupported optional field retained")
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"type":"final","message":"ok"}`}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 17, "completion_tokens": 8}})
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "test", APIKey: "fixture", Usage: func(i, o int) { input = i; output = o }}
	raw, err := p.Generate(context.Background(), Request{Config: DefaultConfig(), System: "test", Prompt: "request"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDecision(raw); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || input != 17 || output != 8 {
		t.Fatalf("calls %d usage %d/%d", calls, input, output)
	}
}
func TestHTTPRejectsPartialAction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"action\"}"},"finish_reason":"length"}]}`))
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "test"}
	if _, err := p.Generate(context.Background(), Request{Config: DefaultConfig()}); err == nil {
		t.Fatal("accepted incomplete action")
	}
}

func TestManagedModelRequiresCompleteDecisionSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Format struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		content := `{"message":"Hola"}` // Valid JSON, incomplete operator decision.
		if body.Format.Type == "json_schema" {
			content = `{"type":"final","message":"Hola"}`
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "assistant", Managed: true}
	raw, err := p.Generate(context.Background(), Request{Config: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDecision(raw); err != nil {
		t.Fatalf("managed decoder accepted an incomplete decision shape: %v", err)
	}
}

func TestHTTPExistingEndpointOptionalParameters(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls == 1 {
			kwargs, ok := body["chat_template_kwargs"].(map[string]any)
			if !ok || kwargs["enable_thinking"] != false {
				t.Error("thinking was not disabled")
			}
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"chat_template_kwargs unsupported"}`))
			return
		}
		if _, ok := body["chat_template_kwargs"]; ok {
			t.Error("unsupported template field retained")
		}
		if calls == 2 {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"response_format unsupported"}`))
			return
		}
		if _, ok := body["response_format"]; ok {
			t.Error("unsupported format field retained")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"final\",\"message\":\"ok\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "test"}
	if _, err := p.Generate(context.Background(), Request{Config: DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("calls %d", calls)
	}
}
