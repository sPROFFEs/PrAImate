package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Field struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}
type Action struct {
	Name              string                                             `json:"name"`
	Description       string                                             `json:"description,omitempty"`
	Capability        string                                             `json:"capability"`
	Fields            map[string]Field                                   `json:"fields"`
	ExtraCapabilities func(map[string]any) []string                      `json:"-"`
	Execute           func(context.Context, map[string]any) (any, error) `json:"-"`
}
type Registry struct{ actions map[string]Action }

func NewRegistry() *Registry { return &Registry{actions: map[string]Action{}} }
func (r *Registry) Register(a Action) {
	if a.Name == "" || a.Execute == nil {
		panic("invalid Assistant action")
	}
	if _, ok := r.actions[a.Name]; ok {
		panic("duplicate Assistant action")
	}
	r.actions[a.Name] = a
}
func (r *Registry) Search(query string, limit int) []Action {
	if limit < 1 || limit > 6 {
		limit = 6
	}
	type match struct {
		a     Action
		score int
	}
	found := []match{}
	for _, a := range r.actions {
		score := 0
		hay := strings.ToLower(a.Name + " " + a.Description)
		for _, term := range strings.Fields(strings.ToLower(query)) {
			if strings.Contains(hay, term) {
				score++
			}
		}
		if score > 0 || query == "" {
			found = append(found, match{a, score})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].score == found[j].score {
			return found[i].a.Name < found[j].a.Name
		}
		return found[i].score > found[j].score
	})
	out := []Action{}
	for _, m := range found {
		if len(out) >= limit {
			break
		}
		out = append(out, m.a)
	}
	return out
}
func (r *Registry) Get(name string) (Action, bool) { a, ok := r.actions[name]; return a, ok }
func (a Action) Validate(input map[string]any) error {
	if input == nil {
		input = map[string]any{}
	}
	for key, v := range input {
		field, ok := a.Fields[key]
		if !ok {
			return fmt.Errorf("unknown argument %q for %s", key, a.Name)
		}
		valid := false
		switch field.Type {
		case "string":
			_, valid = v.(string)
		case "boolean":
			_, valid = v.(bool)
		case "integer":
			n, ok := v.(float64)
			valid = ok && n == float64(int64(n))
		case "array":
			_, valid = v.([]any)
		case "object":
			_, valid = v.(map[string]any)
		}
		if !valid {
			return fmt.Errorf("invalid %s argument %s", field.Type, key)
		}
	}
	for key, field := range a.Fields {
		if field.Required {
			v, ok := input[key]
			if !ok || v == nil || v == "" {
				return fmt.Errorf("missing argument %s", key)
			}
		}
	}
	return nil
}

type Decision struct {
	Type      string         `json:"type"`
	Action    string         `json:"action,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Message   string         `json:"message,omitempty"`
}

func ParseDecision(raw string) (Decision, error) {
	var d Decision
	if len(raw) > 32768 {
		return d, errors.New("Assistant structured response exceeds limit")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return d, fmt.Errorf("invalid structured Assistant response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return d, errors.New("Assistant returned trailing content")
	}
	if d.Type == "final" && strings.TrimSpace(d.Message) != "" && d.Action == "" && len(d.Arguments) == 0 && len(d.Message) <= 8192 {
		return d, nil
	}
	if d.Type == "action" && d.Action != "" && d.Message == "" {
		return d, nil
	}
	return d, errors.New("Assistant must return one action or one final message")
}
