package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHTTPIncludesExecutedActionDialogue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Messages) != 4 || body.Messages[1].Content != "Open settings" || body.Messages[2].Role != "assistant" || body.Messages[3].Role != "user" || !strings.Contains(body.Messages[3].Content, `"opened":"settings"`) {
			t.Error("actual action/result was not delivered as dialogue")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"final\",\"message\":\"Opened settings\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "assistant", Managed: true}
	_, err := p.Generate(context.Background(), Request{Config: DefaultConfig(), Message: "Open settings", Observations: []Observation{{Decision: Decision{Type: "action", Action: "ui.navigate", Arguments: map[string]any{"page": "settings"}}, Result: `{"ok":true,"result":{"opened":"settings"}}`}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeNeverDropsDecisionConstraints(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"response_format unsupported"}`))
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "assistant", Managed: true}
	if _, err := p.Generate(context.Background(), Request{Config: DefaultConfig()}); err == nil || calls != 1 {
		t.Fatal("managed runtime silently disabled structured decoding")
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

func TestManagedSchemaConstrainsDiscoveredNamesAndArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Format struct {
				Definition struct {
					Schema struct {
						Branches []struct {
							Properties struct {
								Type   struct{ Const string }
								Action struct {
									Const string
									Type  string
								}
								Arguments struct {
									Properties           map[string]struct{ Type string }
									Required             []string
									AdditionalProperties bool
								}
							}
						} `json:"oneOf"`
					}
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		branches := body.Format.Definition.Schema.Branches
		if len(branches) != 2 || branches[0].Properties.Type.Const != "final" {
			t.Fatalf("missing conversation choice or extra undiscovered actions: %+v", branches)
		}
		action := branches[1].Properties
		if action.Action.Const != "ui.navigate" || action.Action.Type != "" || action.Arguments.AdditionalProperties || len(action.Arguments.Properties) != 1 || action.Arguments.Properties["page"].Type != "string" || len(action.Arguments.Required) != 1 || action.Arguments.Required[0] != "page" {
			t.Error("schema permits invented actions or invalid arguments")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"final\",\"message\":\"Hola\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	p := HTTPProvider{Endpoint: server.URL, Model: "assistant", Managed: true}
	_, err := p.Generate(context.Background(), Request{Config: DefaultConfig(), Actions: []Action{{Name: "ui.navigate", Fields: map[string]Field{"page": {Type: "string", Required: true}}}}})
	if err != nil {
		t.Fatal(err)
	}
}
