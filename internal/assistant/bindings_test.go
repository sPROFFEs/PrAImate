package assistant

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProjectBindingsPreserveFullPathsAndPreferExplicitRequest(t *testing.T) {
	project := filepath.Join(t.TempDir(), "long-project-folder")
	for _, message := range []string{"Open a terminal in " + project + ".", "Abre el terminal en '" + project + "'.", `Open the terminal in "` + project + `".`} {
		if got := requestProjects(message, map[string]any{"project": t.TempDir()}); !reflect.DeepEqual(got, []string{project}) {
			t.Fatalf("%q: got %v", message, got)
		}
	}
	withSpace := filepath.Join(t.TempDir(), "project folder")
	if got := requestProjects(`Open in "`+withSpace+`"`, nil); !reflect.DeepEqual(got, []string{withSpace}) {
		t.Fatalf("quoted folder changed: %v", got)
	}
	if got := requestProjects("Open a terminal", map[string]any{"project": project}); !reflect.DeepEqual(got, []string{project}) {
		t.Fatalf("active project ignored: %v", got)
	}
}

func TestProjectBindingsConstrainModelAndValidateBeforeExecution(t *testing.T) {
	project := t.TempDir()
	c := DefaultConfig()
	c.Enabled, c.Permissions = true, Preset("full")
	r := NewRegistry()
	executed := 0
	r.Register(Action{Name: "code.start", Description: "Open a Code terminal", Capability: "chats", Fields: map[string]Field{"workspace": {Type: "string", Required: true, InputSource: "project"}}, Execute: func(_ context.Context, args map[string]any) (any, error) {
		executed++
		if args["workspace"] != project {
			t.Fatal("wrong folder executed")
		}
		return map[string]any{"started": true}, nil
	}})
	p := &fakeProvider{responses: []string{`{"type":"action","action":"code.start","arguments":{"workspace":"/tmp"}}`, `{"type":"final","message":"Please confirm the project folder."}`}}
	state := State{}
	s := New(Options{Registry: r, Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(_ context.Context, v State) error { state = v; return nil }})
	result, err := s.Run(context.Background(), "Open a terminal in "+project, nil)
	if err != nil || executed != 0 || result.Task.Status != "needs_input" {
		t.Fatalf("unrequested folder executed: result=%+v err=%v", result, err)
	}
	if got := p.requests[0].Actions[0].Fields["workspace"].Enum; !reflect.DeepEqual(got, []string{project}) {
		t.Fatalf("decoder not bound to complete path: %v", got)
	}
	a, _ := r.Get("code.start")
	if len(a.Fields["workspace"].Enum) != 0 {
		t.Fatal("request binding leaked into registry")
	}
	if err := validateInputBindings(bindActionInputs([]Action{a}, nil, "")[0]); err == nil {
		t.Fatal("allowed invented project path when user supplied none")
	}
}

func TestCLIIdentifierBindingPreservesExplicitChoice(t *testing.T) {
	a := Action{Name: "code.start", Fields: map[string]Field{"cli": {Type: "string", InputSource: "cli", Enum: []string{"codex", "praimate-cli", "claude"}}}}
	bound := bindActionInputs([]Action{a}, nil, "Abre un terminal con praimate-cli en /tmp/project")[0]
	if got := bound.Fields["cli"].Enum; !reflect.DeepEqual(got, []string{"praimate-cli"}) {
		t.Fatalf("selected CLI not bound: %v", got)
	}
	if err := bound.Validate(map[string]any{"cli": "/tmp/project"}); err == nil {
		t.Fatal("allowed a filesystem path as the CLI identifier")
	}
	if err := bound.Validate(map[string]any{"cli": "codex"}); err == nil {
		t.Fatal("allowed a different CLI than the explicit user choice")
	}
}
