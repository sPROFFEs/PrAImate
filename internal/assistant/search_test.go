package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestActionSearchNaturalLanguageAndWholeWords(t *testing.T) {
	r := NewRegistry()
	for _, a := range []Action{
		{Name: "actions.search", Description: "Discover agents chats workers models settings"},
		{Name: "app.search", Description: "Search agents chats workers models settings"},
		{Name: "agents.create", Description: "Create an agent persona"},
		{Name: "agents.delete", Description: "Delete an existing agent"},
		{Name: "workers.set_model", Description: "Change a worker model", Fields: map[string]Field{"tier": {Description: "primary, middle or fast"}}},
		{Name: "settings.appearance", Description: "Change the theme"},
		{Name: "chats.rename", Description: "Rename a conversation"},
		{Name: "ui.navigate", Description: "Open an application page"},
	} {
		a.Execute = func(context.Context, map[string]any) (any, error) {
			t.Fatal("discovery executed an action")
			return nil, nil
		}
		r.Register(a)
	}
	for _, tc := range []struct{ query, want string }{
		{"Crea un agente de test, por favor.", "agents.create"},
		{"Create an agent named test.", "agents.create"},
		{"Cambia el modelo del worker rápido a qwen-test.", "workers.set_model"},
		{"Pon el tema oscuro", "settings.appearance"},
		{"Renombra la conversación a Prueba", "chats.rename"},
		{"Abre los ajustes", "ui.navigate"},
		{"Find my conversation about backups", "app.search"},
	} {
		found := r.Search(tc.query, 6)
		if len(found) == 0 || found[0].Name != tc.want {
			t.Errorf("query %q: got %v, want %s first", tc.query, actionNames(found), tc.want)
		}
	}
	if got := r.Search("la mi the unsupportedword", 6); len(got) != 0 {
		t.Fatalf("stopwords or substrings matched irrelevant actions: %v", actionNames(got))
	}
	for _, a := range r.Search("Change the fast worker model", 6) {
		if !strings.HasPrefix(a.Name, "workers.") {
			t.Fatalf("unrelated domain exposed for a worker request: %s", a.Name)
		}
	}
}

func TestActionSchemaPreservesParameterSemanticsAndEnums(t *testing.T) {
	a := Action{Name: "settings.appearance", Fields: map[string]Field{"theme": {Type: "string", Description: "Use the canonical theme identifier", Required: true, Enum: []string{"light", "dark", "system"}}}}
	if err := a.Validate(map[string]any{"theme": "oscuro"}); err == nil {
		t.Fatal("accepted an unrecognized canonical enum value")
	}
	if err := a.Validate(map[string]any{"theme": "dark"}); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []any{json.RawMessage(actionSchemas([]Action{a})), decisionSchema([]Action{a})} {
		raw, err := json.Marshal(schema)
		if err != nil || !strings.Contains(string(raw), a.Fields["theme"].Description) || !strings.Contains(string(raw), `"enum":["light","dark","system"]`) {
			t.Fatalf("parameter semantics missing: %s; error=%v", raw, err)
		}
	}
}
