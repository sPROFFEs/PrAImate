package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOpenCodeExecutionModesSelectExplicitAgent(t *testing.T) {
	for mode, want := range map[string][]string{"plan": {"--agent", "plan"}, "edits": {"--agent", "build"}, "full": {"--agent", "build", "--dangerously-skip-permissions"}} {
		if got := openCodeModeArgs(mode); !reflect.DeepEqual(got, want) {
			t.Fatalf("mode=%s args=%v want=%v", mode, got, want)
		}
	}
}

func TestOpenCodeEditModeGrantsWorkspaceEditsWithoutCommandAccess(t *testing.T) {
	base := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"model":"fixture/model","provider":{"fixture":{"options":{"baseURL":"http://localhost/v1"}}}}`}
	before := base["OPENCODE_CONFIG_CONTENT"]
	env, err := openCodePermissionEnvironment(base, "edits")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &cfg); err != nil {
		t.Fatal(err)
	}
	p := cfg["permission"].(map[string]any)
	if p["edit"] != "allow" || p["read"] != "allow" || p["bash"] != "deny" || p["external_directory"] != "deny" || p["*"] != "deny" || cfg["model"] != "fixture/model" || cfg["provider"] == nil || base["OPENCODE_CONFIG_CONTENT"] != before {
		t.Fatalf("wrong scoped permissions: %v", cfg)
	}
}
