package assistant

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var requestWords = regexp.MustCompile(`"([^"]+)"|'([^']+)'|[^\s"']+`)

// Bind literal project paths before decoding. Tiny models can shorten a long
// path to /tmp while still emitting valid JSON; an enum preserves the complete
// user-supplied path. Explicit request paths take precedence over UI context.
func requestProjects(message string, ui map[string]any) []string {
	paths := []string{}
	seen := map[string]bool{}
	add := func(path string) {
		if filepath.IsAbs(path) && !seen[path] && len(paths) < 4 {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	for _, match := range requestWords.FindAllStringSubmatch(message, -1) {
		path := match[0]
		if match[1] != "" {
			path = match[1]
		} else if match[2] != "" {
			path = match[2]
		} else {
			path = strings.TrimLeft(path, "(")
			if info, err := os.Stat(path); err != nil || !info.IsDir() {
				path = strings.TrimRight(path, ".,;:!?)]")
			}
		}
		add(path)
	}
	if len(paths) == 0 {
		project, _ := ui["project"].(string)
		add(project)
	}
	return paths
}

func bindActionInputs(actions []Action, projects []string, message string) []Action {
	out := make([]Action, 0, len(actions))
	for _, action := range actions {
		copy := action
		copy.Fields = make(map[string]Field, len(action.Fields))
		for name, field := range action.Fields {
			if field.InputSource == "project" {
				field.Enum = projects
			}
			if field.InputSource == "cli" {
				selected := []string{}
				for _, choice := range field.Enum {
					for _, word := range requestWords.FindAllString(message, -1) {
						if strings.EqualFold(strings.Trim(word, "\"'.,;:!?()[]"), choice) {
							selected = append(selected, choice)
							break
						}
					}
				}
				if len(selected) > 0 {
					field.Enum = selected
				}
			}
			copy.Fields[name] = field
		}
		out = append(out, copy)
	}
	return out
}

func validateInputBindings(action Action) error {
	for name, field := range action.Fields {
		if field.InputSource == "project" && len(field.Enum) == 0 {
			return fmt.Errorf("missing project folder for %s; ask the user for its absolute path", name)
		}
	}
	return nil
}
