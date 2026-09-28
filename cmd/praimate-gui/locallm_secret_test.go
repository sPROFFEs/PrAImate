package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestMigrateLegacyLocalLLMAPIKeyMovesSecretIntoEncryptedDB(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	cfg := &launcher.Config{
		DefaultLocalEndpoint: "https://llm.example",
		DefaultLocalAPIKey:   "legacy-plaintext-secret",
	}
	if err := launcher.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyLocalLLMAPIKey(c, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadLocalLLMAPIKey(c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "legacy-plaintext-secret" {
		t.Fatalf("encrypted setting = %q", got)
	}
	_, configPath, _ := launcher.ConfigPaths()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-plaintext-secret") {
		t.Fatalf("plaintext config retained API key:\n%s", raw)
	}
	if rawSetting, err := c.GetSetting(context.Background(), core.ScopeCLI, localLLMAPIKeySetting); err != nil || rawSetting == nil {
		t.Fatalf("encrypted setting missing: raw=%s err=%v", rawSetting, err)
	}
}

func TestGetLocalLLMDoesNotReturnDecryptedCredential(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "https://llm.example"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := saveLocalLLMAPIKey(c, "renderer-must-not-see-this"); err != nil {
		t.Fatal(err)
	}

	a := &App{core: c}
	got, err := a.GetLocalLLM()
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "" || !got.HasAPIKey {
		t.Fatalf("GetLocalLLM exposed credential or lost presence marker: %+v", got)
	}
}

func TestSetLocalLLMBlankCredentialPreservesExistingSecret(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	if err := launcher.SaveConfig(&launcher.Config{}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	if err := saveLocalLLMAPIKey(c, "keep-me"); err != nil {
		t.Fatal(err)
	}
	a := &App{core: c}
	if err := a.SetLocalLLM(LocalLLMDefaults{Endpoint: "https://llm.example"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadLocalLLMAPIKey(c); got != "keep-me" {
		t.Fatalf("blank renderer field cleared saved credential: %q", got)
	}
	if err := a.SetLocalLLM(LocalLLMDefaults{Endpoint: "https://llm.example", RemoveAPIKey: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadLocalLLMAPIKey(c); got != "" {
		t.Fatalf("explicit removal retained credential: %q", got)
	}
}

func TestRedactChatCredentialKeepsSecretOutOfWailsPayload(t *testing.T) {
	chat := &core.Chat{Settings: core.ChatSettings{Local: &core.ChatLocalEndpoint{
		Endpoint: "https://llm.example", APIKey: "chat-secret", Model: "qwen3",
	}}}
	got := redactChatCredential(chat)
	if got.Settings.Local.APIKey != "" {
		t.Fatalf("redacted chat exposed API key: %+v", got.Settings.Local)
	}
	if got.Settings.Local.Endpoint != "https://llm.example" || got.Settings.Local.Model != "qwen3" {
		t.Fatalf("redaction removed non-secret route data: %+v", got.Settings.Local)
	}
}
