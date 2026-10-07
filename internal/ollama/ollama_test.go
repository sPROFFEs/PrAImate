package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeEndpoint(t *testing.T) {
	cases := map[string]string{
		"192.168.1.10:11434":         "http://192.168.1.10:11434",
		"http://x.local:11434/":      "http://x.local:11434",
		"  http://x.local:11434  ":   "http://x.local:11434",
		"https://ollama.example.com": "https://ollama.example.com",
		"":                           "",
		// Real bug the user hit: a missing-slashes typo in the scheme.
		// Without the fix, the value would later be prepended with
		// another "http://" downstream, producing "http://http:host:port".
		"http:192.168.1.42:11434":  "http://192.168.1.42:11434",
		"https:example.com":        "https://example.com",
		"HTTP:192.168.1.10:11434":  "http://192.168.1.10:11434",
		"http:/lonely.slash:11434": "http://lonely.slash:11434",
	}
	for in, want := range cases {
		if got := NormalizeEndpoint(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListModels_PrefersOllamaTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]string{
					{"name": "qwen3-coder"},
					{"name": "llama3.1:8b"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	models, err := ListModels(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "llama3.1:8b" || models[1] != "qwen3-coder" {
		t.Errorf("got %v, want sorted [llama3.1:8b qwen3-coder]", models)
	}
}

func TestListModels_FallsBackToOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			http.NotFound(w, r) // simulate non-Ollama backend
			return
		}
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "phi3"}, {"id": "qwen"}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	models, _ := ListModels(context.Background(), srv.URL, "")
	if len(models) != 2 {
		t.Fatalf("got %v", models)
	}
}

func TestListModels_DoesNotDuplicateSavedV1(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	models, err := ListModels(context.Background(), srv.URL+"/v1", "")
	if err != nil || len(models) != 1 || models[0] != "qwen" {
		t.Fatalf("models=%v err=%v paths=%v", models, err, paths)
	}
	for _, path := range paths {
		if strings.Contains(path, "/v1/v1/") {
			t.Fatalf("duplicated /v1 in discovery path: %v", paths)
		}
	}
}

func TestListCanonicalModelsPrefersRoutableIDsAndFallsBack(t *testing.T) {
	canonical := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("prefix") == "canonical" {
			if !canonical {
				http.Error(w, "unsupported", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "openai/qwen"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen"}}})
	}))
	defer srv.Close()
	models, err := ListCanonicalModels(context.Background(), srv.URL+"/v1", "")
	if err != nil || len(models) != 1 || models[0] != "openai/qwen" {
		t.Fatalf("canonical models: %v, %v", models, err)
	}
	canonical = false
	models, err = ListCanonicalModels(context.Background(), srv.URL, "")
	if err != nil || len(models) != 1 || models[0] != "qwen" {
		t.Fatalf("fallback models: %v, %v", models, err)
	}
}

func TestOpenClaudeEnvUsesOpenAICompatibleRoute(t *testing.T) {
	env := OpenClaudeEnv(Settings{
		Endpoint: "https://llm.example/v1/", Model: "qwen3", APIKey: "secret",
		ContextTokens: 4096, OutputTokens: 1024,
	})
	if env["CLAUDE_CODE_USE_OPENAI"] != "1" || env["OPENAI_MODEL"] != "qwen3" {
		t.Fatalf("OpenClaude mode variables = %#v", env)
	}
	if env["OPENAI_BASE_URL"] != "https://llm.example/v1" || env["OPENAI_API_KEY"] != "secret" {
		t.Fatalf("OpenClaude route variables = %#v", env)
	}
	if !strings.Contains(env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"], `"qwen3":4096`) {
		t.Fatalf("OpenClaude context limit = %q", env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"])
	}
	if !strings.Contains(env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"], `"qwen3":1024`) {
		t.Fatalf("OpenClaude output limit = %q", env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"])
	}
}

func TestOpenAIEnvDoesNotDuplicateV1(t *testing.T) {
	env := OpenAIEnv(Settings{Endpoint: "https://llm.example/v1", Model: "qwen3"})
	if env["OPENAI_BASE_URL"] != "https://llm.example/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q", env["OPENAI_BASE_URL"])
	}
	if env["OPENAI_API_KEY"] != "ollama" {
		t.Fatalf("OPENAI_API_KEY = %q", env["OPENAI_API_KEY"])
	}
}

func TestApplyOpenCodeDoesNotDuplicateV1(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := ApplyOpenCode(Settings{Endpoint: "https://llm.example/proxy/v1/", Model: "qwen"}, true); err != nil {
		t.Fatal(err)
	}
	route, err := ConfiguredOpenCodeRoute("praimate_local/qwen", "")
	if err != nil || route == nil || route.Endpoint != "https://llm.example/proxy/v1" {
		t.Fatalf("incorrect registered API root: %v %v", route, err)
	}
	if OpenAIEndpoint("https://llm.example/proxy") != OpenAIEndpoint("https://llm.example/proxy/v1/") {
		t.Fatal("server URL and API root should identify the same provider")
	}
}

// TestListModels_SendsBearerWhenKeySet verifies the probe forwards
// Authorization: Bearer <key> when the user supplied one. GPUStack and
// other gated providers reject /v1/models without it.
func TestListModels_SendsBearerWhenKeySet(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		// Refuse /api/tags so we exercise the /v1/models path too.
		if r.URL.Path == "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "qwen3-0.6b"}},
		})
	}))
	defer srv.Close()

	if _, err := ListModels(context.Background(), srv.URL, "sk-test-key"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test-key" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer sk-test-key")
	}
}

// TestApplyOpenCode_ReferencesAPIKeyEnvironment: the plaintext config
// contains only an environment reference, never the credential itself.
func TestApplyOpenCode_ReferencesAPIKeyEnvironment(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	path, err := ApplyOpenCode(Settings{Endpoint: "10.0.0.1:11434", Model: "qwen3", APIKey: "sk-abc"}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	prov := cfg["provider"].(map[string]any)
	entry := prov["praimate_local"].(map[string]any)
	opts := entry["options"].(map[string]any)
	if opts["apiKey"] != "{env:"+OpenCodeAPIKeyEnv("praimate_local")+"}" {
		t.Errorf("options.apiKey = %v, want environment reference", opts["apiKey"])
	}
	if strings.Contains(string(raw), "sk-abc") {
		t.Fatalf("opencode config leaked API key:\n%s", raw)
	}
}

func TestOpenCodeUsesManagedLocalRoute(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	if got, err := OpenCodeUsesManagedLocalRoute("praimate_local/qwen3"); err != nil || !got {
		t.Fatalf("explicit managed model: got %v, err %v", got, err)
	}
	if got, err := OpenCodeUsesManagedLocalRoute("openai/gpt-5"); err != nil || got {
		t.Fatalf("explicit cloud model: got %v, err %v", got, err)
	}
	if _, err := ApplyOpenCode(Settings{Endpoint: "h:1", Model: "qwen3"}, true); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenCodeUsesManagedLocalRoute(""); err != nil || !got {
		t.Fatalf("configured managed default: got %v, err %v", got, err)
	}
}

func TestApplyOpenCode_WritesModelTokenLimits(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	path, err := ApplyOpenCode(Settings{
		Endpoint:      "10.0.0.1:11434",
		Model:         "qwen3",
		ContextTokens: 4096,
		OutputTokens:  1024,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	prov := cfg["provider"].(map[string]any)
	entry := prov["praimate_local"].(map[string]any)
	models := entry["models"].(map[string]any)
	model := models["qwen3"].(map[string]any)
	limit := model["limit"].(map[string]any)
	if limit["context"] != float64(4096) || limit["output"] != float64(1024) {
		t.Errorf("limit = %#v, want context=4096 output=1024\n%s", limit, raw)
	}
}

// TestApplyOpenCode_OmitsAPIKeyWhenBlank: vanilla Ollama path — no
// apiKey field should appear in the options block.
func TestApplyOpenCode_OmitsAPIKeyWhenBlank(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	path, _ := ApplyOpenCode(Settings{Endpoint: "h:1", Model: "m"}, false)
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"apiKey"`) {
		t.Errorf("opencode.json should omit apiKey when blank:\n%s", raw)
	}
}

// TestApplyDeepSeek_ReferencesKeyEnvironment keeps the credential out of
// DeepSeek's plaintext TOML.
func TestApplyDeepSeek_ReferencesKeyEnvironment(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	path, _ := ApplyDeepSeek(Settings{Endpoint: "h:1", Model: "m", APIKey: "sk-deepseek"})
	body, _ := os.ReadFile(path)
	if !strings.Contains(string(body), `api_key_env = "OPENAI_API_KEY"`) {
		t.Errorf("expected api_key_env in managed block:\n%s", body)
	}
	if strings.Contains(string(body), "sk-deepseek") {
		t.Errorf("DeepSeek config leaked API key:\n%s", body)
	}

	// Re-apply without a key: the api_key line must disappear.
	if _, err := ApplyDeepSeek(Settings{Endpoint: "h:1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(path)
	if strings.Contains(string(body2), "api_key") {
		t.Errorf("re-apply without key should drop the api_key line:\n%s", body2)
	}
}

// redirectHome points UserHomeDir at a temp dir for this test only.
func redirectHome(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	} else {
		t.Setenv("HOME", dir)
	}
}

func TestDisableCodex_RemovesBlocks(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	path, _ := CodexConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `model_provider = "praimate_local"
model = "qwen3"
approval_policy = "on-request"

[model_providers.praimate_local]
base_url = "http://x/v1"

[profiles.praimate_local]
model_provider = "praimate_local"

[profiles.keep]
model = "gpt-5"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DisableCodex(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "praimate_local") {
		t.Errorf("disable left praimate_local:\n%s", raw)
	}
	if strings.Contains(string(raw), `model = "qwen3"`) {
		t.Errorf("disable left managed top-level model:\n%s", raw)
	}
	if !strings.Contains(string(raw), `approval_policy = "on-request"`) {
		t.Errorf("disable removed unrelated top-level config:\n%s", raw)
	}
	if !strings.Contains(string(raw), "[profiles.keep]") {
		t.Errorf("disable removed unrelated config:\n%s", raw)
	}
}

func TestApplyOpenCode_WritesProviderJSON(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))

	path, err := ApplyOpenCode(Settings{Endpoint: "10.0.0.1:11434", Model: "qwen3"}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, raw)
	}
	if cfg["$schema"] != "https://opencode.ai/config.json" {
		t.Errorf("missing $schema")
	}
	if cfg["model"] != "praimate_local/qwen3" {
		t.Errorf("model = %v", cfg["model"])
	}
	provider := cfg["provider"].(map[string]any)
	if _, ok := provider["praimate_local"]; !ok {
		t.Error("praimate_local provider not added")
	}
}

func TestApplyOpenCode_PreservesOtherProviders(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	path, _ := OpenCodeConfigPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	pre, _ := json.Marshal(map[string]any{
		"$schema":  "x",
		"model":    "openai/gpt-5",
		"provider": map[string]any{"my_provider": map[string]any{"name": "Mine"}},
	})
	if err := os.WriteFile(path, pre, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyOpenCode(Settings{Endpoint: "x:1", Model: "m"}, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	prov := cfg["provider"].(map[string]any)
	if _, ok := prov["my_provider"]; !ok {
		t.Error("my_provider removed")
	}
	if _, ok := prov["praimate_local"]; !ok {
		t.Error("praimate_local not added")
	}
	// makeDefault=false → existing model untouched.
	if cfg["model"] != "openai/gpt-5" {
		t.Errorf("model overwritten: %v", cfg["model"])
	}
}

func TestOpenCodeConfigured_ReflectsDiskState(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	if OpenCodeConfigured() {
		t.Fatal("fresh dir: OpenCodeConfigured should be false")
	}
	if _, err := ApplyOpenCode(Settings{Endpoint: "x:1", Model: "m"}, false); err != nil {
		t.Fatal(err)
	}
	if !OpenCodeConfigured() {
		t.Error("after ApplyOpenCode: OpenCodeConfigured should be true")
	}
	if _, err := DisableOpenCode(); err != nil {
		t.Fatal(err)
	}
	if OpenCodeConfigured() {
		t.Error("after DisableOpenCode: OpenCodeConfigured should be false")
	}
}

func TestDisableOpenCode_RemovesProvider(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg"))
	if _, err := ApplyOpenCode(Settings{Endpoint: "x:1", Model: "m"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := DisableOpenCode(); err != nil {
		t.Fatal(err)
	}
	path, _ := OpenCodeConfigPath()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "praimate_local") {
		t.Errorf("disable left praimate_local:\n%s", raw)
	}
}

func TestApplyDeepSeek_WritesManagedBlock(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)

	path, err := ApplyDeepSeek(Settings{
		Endpoint: "192.168.1.10:11434",
		Model:    "deepseek-coder:1.3b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join(".deepseek", "config.toml")) {
		t.Errorf("config path %q doesn't end in .deepseek/config.toml", path)
	}
	body, _ := os.ReadFile(path)
	for _, want := range []string{
		"# >>> praimate ollama config",
		`provider = "ollama"`,
		`model = "deepseek-coder:1.3b"`,
		"[providers.ollama]",
		`base_url = "http://192.168.1.10:11434/v1"`,
		"# <<< praimate ollama config",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config missing %q\n%s", want, body)
		}
	}

	// Re-apply should be idempotent — exactly one managed block.
	if _, err := ApplyDeepSeek(Settings{
		Endpoint: "192.168.1.10:11434",
		Model:    "deepseek-coder:1.3b",
	}); err != nil {
		t.Fatal(err)
	}
	body2, _ := os.ReadFile(path)
	if got := strings.Count(string(body2), "# >>> praimate ollama config"); got != 1 {
		t.Errorf("expected exactly one managed block after re-apply, got %d:\n%s", got, body2)
	}
}

func TestApplyDeepSeek_PreservesSurroundingConfig(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	dsDir := filepath.Join(tmp, ".deepseek")
	_ = os.MkdirAll(dsDir, 0o755)
	preexisting := `# user notes
theme = "dark"

[providers.openai]
api_key_env = "OPENAI_API_KEY"
`
	cfgPath := filepath.Join(dsDir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(preexisting), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDeepSeek(Settings{Endpoint: "host:11434", Model: "qwen"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(cfgPath)
	for _, want := range []string{
		`theme = "dark"`,
		"[providers.openai]",
		`api_key_env = "OPENAI_API_KEY"`,
		"# >>> praimate ollama config",
		`[providers.ollama]`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("config missing %q after apply:\n%s", want, body)
		}
	}
}

func TestDeepSeekConfigured_ReflectsDiskState(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)

	if DeepSeekConfigured() {
		t.Error("DeepSeekConfigured should be false before Apply")
	}
	if _, err := ApplyDeepSeek(Settings{Endpoint: "h:1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if !DeepSeekConfigured() {
		t.Error("DeepSeekConfigured should be true after Apply")
	}
	if _, err := DisableDeepSeek(); err != nil {
		t.Fatal(err)
	}
	if DeepSeekConfigured() {
		t.Error("DeepSeekConfigured should be false after Disable")
	}
}

func TestDisableDeepSeek_RemovesOnlyManagedBlock(t *testing.T) {
	tmp := t.TempDir()
	redirectHome(t, tmp)
	dsDir := filepath.Join(tmp, ".deepseek")
	_ = os.MkdirAll(dsDir, 0o755)
	other := "theme = \"dark\"\n[providers.openai]\nkey = \"x\"\n"
	cfgPath := filepath.Join(dsDir, "config.toml")
	_ = os.WriteFile(cfgPath, []byte(other), 0o644)

	if _, err := ApplyDeepSeek(Settings{Endpoint: "h:1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DisableDeepSeek(); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(body), "# >>> clade ollama config") {
		t.Error("Disable should remove the managed block")
	}
	for _, keep := range []string{`theme = "dark"`, "[providers.openai]"} {
		if !strings.Contains(string(body), keep) {
			t.Errorf("Disable removed unrelated config %q:\n%s", keep, body)
		}
	}
}

// Ensure the io.Copy helper compiles + works (covers a tiny export).
func TestCopyTo(t *testing.T) {
	var buf strings.Builder
	n, err := CopyTo(&buf, strings.NewReader("hello"))
	if err != nil || n != 5 || buf.String() != "hello" {
		t.Errorf("CopyTo broken: n=%d err=%v out=%q", n, err, buf.String())
	}
}

var _ = io.Discard // keep import even if dropped in edits

// A config carrying the PRE-REBRAND managed block must be replaced (not
// duplicated) when re-applying — legacy markers are still stripped.
func TestApplyDeepSeek_ReplacesLegacyCladeBlock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	path, err := DeepSeekConfigPath()
	if err != nil {
		t.Skipf("no deepseek config path on this platform: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "keep_me = true\n# >>> clade ollama config\nold = \"block\"\n# <<< clade ollama config\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyDeepSeek(Settings{Endpoint: "127.0.0.1:11434", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), ">>> clade ollama config") {
		t.Errorf("legacy block survived re-apply:\n%s", body)
	}
	if !strings.Contains(string(body), ">>> praimate ollama config") {
		t.Errorf("new managed block missing:\n%s", body)
	}
	if !strings.Contains(string(body), "keep_me = true") {
		t.Errorf("surrounding config lost:\n%s", body)
	}
}
