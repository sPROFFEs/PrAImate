package core

import (
	"context"
	"testing"
)

func TestLocalHostsKeepSecretsOutOfReadsAndPreserveDesktopDefaultKey(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	host, err := c.SaveLocalHost(ctx, LocalHost{Name: "Ollama", Endpoint: "http://localhost:11434", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if !host.IsDefault || !host.HasAPIKey || host.APIKey != "" {
		t.Fatalf("unsafe saved host: %+v", host)
	}
	rows, err := c.ListLocalHosts(ctx)
	if err != nil || len(rows) != 1 || rows[0].APIKey != "" || !rows[0].HasAPIKey {
		t.Fatalf("unsafe host list: %+v, %v", rows, err)
	}
	raw, err := c.GetSetting(ctx, ScopeCLI, "local_llm.api_key")
	if err != nil || string(raw) != `"secret"` {
		t.Fatalf("default secret did not use Desktop key: %q, %v", raw, err)
	}
}
