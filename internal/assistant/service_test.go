package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakeProvider struct {
	responses []string
	requests  []Request
	index     int
}

func TestDiscoveryAloneDoesNotCompleteUserOperation(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	r := NewRegistry()
	r.Register(Action{Name: "actions.search", Capability: "read", Fields: map[string]Field{"query": {Type: "string", Required: true}}, Execute: func(context.Context, map[string]any) (any, error) {
		return []Action{{Name: "chats.list", Capability: "read"}}, nil
	}})
	p := &fakeProvider{responses: []string{`{"type":"action","action":"actions.search","arguments":{"query":"list chats"}}`, `{"type":"final","message":"No chats found."}`}}
	state := State{}
	s := New(Options{Registry: r, Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(_ context.Context, value State) error { state = value; return nil }})
	result, err := s.Run(context.Background(), "List my chats", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Status != "needs_input" || !strings.Contains(result.Messages[len(result.Messages)-1].Text, "no app operation was executed") {
		t.Fatalf("discovery was misreported as task completion: %+v", result)
	}
}

func TestEmptyDiscoveryRetainsDiscoveredActionsAndStructuredFeedback(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	r := NewRegistry()
	searches := 0
	r.Register(Action{Name: "actions.search", Capability: "read", Fields: map[string]Field{"query": {Type: "string", Required: true}}, Execute: func(context.Context, map[string]any) (any, error) {
		searches++
		if searches == 1 {
			a, _ := r.Get("ui.navigate")
			return []Action{a}, nil
		}
		return []Action{}, nil
	}})
	r.Register(Action{Name: "app.search", Capability: "read", Execute: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	r.Register(Action{Name: "ui.navigate", Capability: "navigate", Fields: map[string]Field{"page": {Type: "string", Required: true}}, Execute: func(context.Context, map[string]any) (any, error) { return map[string]any{"opened": "settings"}, nil }})
	p := &fakeProvider{responses: []string{`{"type":"action","action":"actions.search","arguments":{"query":"open settings"}}`, `{"type":"action","action":"actions.search","arguments":{"query":"missing"}}`, `{"type":"action","action":"ui.navigate","arguments":{"page":"settings"}}`, `{"type":"final","message":"Opened settings."}`}}
	state := State{}
	s := New(Options{Registry: r, Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(_ context.Context, value State) error { state = value; return nil }})
	result, err := s.Run(context.Background(), "Open settings", nil)
	if err != nil || result.Task.Status != "completed" {
		t.Fatalf("discovery interrupted navigation: %v", err)
	}
	for _, name := range []string{"actions.search", "ui.navigate"} {
		if !containsAction(p.requests[2].Actions, name) {
			t.Fatalf("empty discovery removed %s", name)
		}
	}
	var prompt map[string]any
	if err := json.Unmarshal([]byte(p.requests[2].Prompt), &prompt); err != nil {
		t.Fatal(err)
	}
	if len(p.requests[2].Observations) == 0 || !json.Valid([]byte(p.requests[2].Observations[1].Result)) {
		t.Fatal("discovery result lost from the actual action dialogue")
	}
}

func (p *fakeProvider) Start(context.Context) error  { return nil }
func (p *fakeProvider) Stop(context.Context) error   { return nil }
func (p *fakeProvider) Health(context.Context) error { return nil }
func (p *fakeProvider) Generate(ctx context.Context, r Request) (string, error) {
	p.requests = append(p.requests, r)
	if p.index >= len(p.responses) {
		return "", errors.New("no response")
	}
	out := p.responses[p.index]
	p.index++
	return out, nil
}
func TestStrictDecision(t *testing.T) {
	for _, raw := range []string{`{"type":"action","action":"x","arguments":{}} garbage`, `{"type":"final","message":"ok"}{}`, "```json\n{}\n```", `{"type":"action","action":"x","unknown":true}`, `{"type":"action","action":"x","message":"also final"}`} {
		if _, err := ParseDecision(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := ParseDecision(`{"type":"final","message":"ok"}`); err != nil {
		t.Fatal(err)
	}
}
func TestIndependentPresets(t *testing.T) {
	for _, name := range []string{"read-only", "standard", "full"} {
		p := Preset(name)
		for _, cap := range []string{"system", "network", "filesystem"} {
			if p[cap] != Deny {
				t.Fatalf("%s grants %s", name, cap)
			}
		}
	}
	c := DefaultConfig()
	if c.Enabled || c.Voice.Enabled || c.Voice.AutoSend {
		t.Fatal("features must opt in")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestPermissionAndCheckpointBeforeExecution(t *testing.T) {
	for _, scenario := range []string{"deny", "ask-revoked", "checkpoint-failed", "success"} {
		t.Run(scenario, func(t *testing.T) {
			c := DefaultConfig()
			c.Enabled = true
			c.Permissions["chats"] = Allow
			if scenario == "deny" {
				c.Permissions["chats"] = Deny
			}
			if scenario == "ask-revoked" {
				c.Permissions["chats"] = Ask
			}
			state := State{}
			executed := 0
			r := NewRegistry()
			r.Register(Action{Name: "chats.rename", Capability: "chats", Fields: map[string]Field{"id": {Type: "string", Required: true}}, Execute: func(context.Context, map[string]any) (any, error) {
				executed++
				return map[string]any{"updated": true}, nil
			}})
			p := &fakeProvider{responses: []string{`{"type":"action","action":"chats.rename","arguments":{"id":"one"}}`, `{"type":"final","message":"done"}`}}
			service := New(Options{Registry: r, Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(ctx context.Context, s State) error {
				if scenario == "checkpoint-failed" && len(s.Task.Steps) > 0 {
					return errors.New("disk unavailable")
				}
				state = s
				return nil
			}, Approve: func(context.Context, string, map[string]any) (bool, error) {
				c.Permissions["chats"] = Deny
				return true, nil
			}})
			result, err := service.Run(context.Background(), "Rename one", nil)
			if scenario == "success" {
				if err != nil || executed != 1 || result.Task.Status != "completed" {
					t.Fatalf("%v executed=%d state=%+v", err, executed, result)
				}
			} else if executed != 0 {
				t.Fatal("executed without effective permission and checkpoint")
			}
			if len(state.Activity) > 0 && strings.Contains(strings.Join(state.Activity[0].ArgumentKeys, ","), "one") {
				t.Fatal("audit includes argument values")
			}
		})
	}
}
func TestInvalidArgumentsNeverExecute(t *testing.T) {
	a := Action{Name: "test", Fields: map[string]Field{"id": {Type: "string", Required: true}}}
	for _, args := range []map[string]any{{"id": false}, {"id": "x", "extra": true}, {}} {
		if a.Validate(args) == nil {
			t.Fatal("accepted invalid arguments")
		}
	}
}
func TestRequestPreserved(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	state := State{Task: &Task{Goal: strings.Repeat("old", 10000)}}
	message := strings.Repeat("current", 500)
	p := &fakeProvider{responses: []string{`{"type":"final","message":"ok"}`}}
	s := New(Options{Registry: NewRegistry(), Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(context.Context, State) error { return nil }})
	if _, err := s.Run(context.Background(), message, nil); err != nil {
		t.Fatal(err)
	}
	if p.requests[0].Message != message {
		t.Fatal("current request truncated")
	}
}

func TestBareConversationNeverExecutesActions(t *testing.T) {
	for _, message := range []string{"hola", " Hello! ", "¿Qué puedes hacer en PrAImate?", "gracias", "hola, abre ajustes"} {
		t.Run(message, func(t *testing.T) {
			c := DefaultConfig()
			c.Enabled = true
			executed := 0
			r := NewRegistry()
			r.Register(Action{Name: "ui.navigate", Capability: "navigate", Fields: map[string]Field{"page": {Type: "string", Required: true}}, Execute: func(context.Context, map[string]any) (any, error) { executed++; return nil, nil }})
			p := &fakeProvider{responses: []string{`{"type":"action","action":"ui.navigate","arguments":{"page":"settings"}}`, `{"type":"final","message":"Hola"}`}}
			state := State{}
			s := New(Options{Registry: r, Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(_ context.Context, value State) error { state = value; return nil }})
			result, err := s.Run(context.Background(), message, map[string]any{"page": "dashboard"})
			if err != nil || result.Task.Status != "completed" {
				t.Fatalf("conversation failed: %v", err)
			}
			want := 0
			if message == "hola, abre ajustes" {
				want = 1
			}
			if executed != want {
				t.Fatalf("executed %d actions for %q; want %d", executed, message, want)
			}
		})
	}
}
