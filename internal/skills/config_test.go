package skills

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSkillConfigYAMLUsesSameStrictContract(t *testing.T) {
	config := testSkillConfig(SkillBinding{Ref: "local/test", Activation: "manual", Optional: false})
	body, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := ParseSkillConfigYAML(body)
	if err != nil || !reflect.DeepEqual(actual, config) {
		t.Fatal("YAML roundtrip", err)
	}
	for _, bad := range []string{
		string(body) + "trusted: true\n",
		string(body) + "---\ntrusted: true\n",
		strings.Replace(string(body), "optional: false", "permissions: full", 1),
		strings.Replace(string(body), "configured: true", "configured: &flag true", 1),
		strings.Replace(string(body), "schema: praimate.skills-config/v1", "schema: !unrecognized praimate.skills-config/v1", 1),
		string(body) + "schema: praimate.skills-config/v1\n",
	} {
		if _, err := ParseSkillConfigYAML([]byte(bad)); err == nil {
			t.Fatalf("unsafe YAML config accepted: %s", bad)
		}
	}
}

func testSkillBudget() SkillBudget {
	return SkillBudget{CatalogTokens: 100, BodyTokens: 200, ResourceTokens: 300, TotalTokens: 600, MaxActive: 4, MaxLoadCallsPerTurn: 5, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 4096}
}
func testSkillConfig(bindings ...SkillBinding) *SkillConfig {
	if bindings == nil {
		bindings = []SkillBinding{}
	}
	return &SkillConfig{Schema: SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: bindings, Budget: testSkillBudget()}
}

func TestSkillConfigStrictWireContract(t *testing.T) {
	valid := testSkillConfig(SkillBinding{Ref: "local/test", Activation: "auto", Optional: false})
	body, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip SkillConfig
	if err := json.Unmarshal(body, &roundtrip); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { delete(m, "configured") },
		func(m map[string]any) { m["configured"] = nil },
		func(m map[string]any) { m["trusted"] = true },
		func(m map[string]any) { m["bindings"] = nil },
		func(m map[string]any) { m["configured"] = false },
		func(m map[string]any) { m["lockfile"] = "../secret" },
		func(m map[string]any) { m["enforcement"] = "native-strict" },
		func(m map[string]any) { b := m["bindings"].([]any)[0].(map[string]any); delete(b, "optional") },
		func(m map[string]any) { b := m["bindings"].([]any)[0].(map[string]any); b["permissions"] = "full" },
		func(m map[string]any) { b := m["bindings"].([]any)[0].(map[string]any); b["activation"] = "enabled" },
		func(m map[string]any) { b := m["budget"].(map[string]any); delete(b, "catalog_tokens") },
		func(m map[string]any) { b := m["budget"].(map[string]any); b["max_active"] = 0 },
		func(m map[string]any) { b := m["budget"].(map[string]any); b["total_tokens"] = 1.5 },
		func(m map[string]any) { b := m["budget"].(map[string]any); b["body_tokens"] = nil },
	} {
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		mutate(fields)
		bad, _ := json.Marshal(fields)
		if err := json.Unmarshal(bad, &roundtrip); err == nil {
			t.Fatalf("invalid configuration accepted: %s", bad)
		}
	}
	empty := testSkillConfig()
	wire, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire, &roundtrip); err != nil || !roundtrip.Configured || roundtrip.Bindings == nil || len(roundtrip.Bindings) != 0 {
		t.Fatal("explicit empty lost", err)
	}
	duplicate := append([]byte(`{"schema":"ignored",`), body[1:]...)
	if err := json.Unmarshal(duplicate, &roundtrip); err == nil {
		t.Fatal("duplicate config key accepted")
	}
}

func TestFreezeSkillPreferencesPreservesExplicitEmptyAndIndependence(t *testing.T) {
	app := SkillScope{Config: testSkillConfig(SkillBinding{Ref: "local/test", Activation: "pinned"}), Lock: []byte("ORIGINAL")}
	selected, _, err := FreezeSkillPreferences(SkillScopes{Application: app})
	if err != nil {
		t.Fatal(err)
	}
	app.Config.Bindings[0].Activation = "off"
	app.Lock[0] = 'X'
	if selected.Config.Bindings[0].Activation != "pinned" || string(selected.Lock) != "ORIGINAL" {
		t.Fatal("session defaults share caller memory")
	}
	empty := SkillScope{Config: testSkillConfig()}
	selected, _, err = FreezeSkillPreferences(SkillScopes{Application: app, Session: empty})
	if err != nil || selected.Config == nil || !selected.Config.Configured || selected.Config.Bindings == nil || len(selected.Config.Bindings) != 0 {
		t.Fatal("explicit none restored defaults", err)
	}
	inherited := testSkillConfig()
	inherited.Configured = false
	selected, _, err = FreezeSkillPreferences(SkillScopes{Application: app, Session: SkillScope{Config: inherited}})
	if err != nil || len(selected.Config.Bindings) != 1 {
		t.Fatal("inheritance did not resolve", err)
	}
}
