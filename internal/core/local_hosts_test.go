package core

import (
	"context"
	"slices"
	"testing"
)

func TestLocalHostDefaultChangesPreserveCredentialOwnership(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	a, err := c.SaveLocalHost(ctx, LocalHost{ID: "a", Endpoint: "https://a.example", APIKey: "a-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.SaveLocalHost(ctx, LocalHost{ID: "b", Endpoint: "https://b.example", APIKey: "b-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	b.IsDefault = true
	if _, err := c.SaveLocalHost(ctx, *b); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id          string
		defaultHost bool
		key         string
	}{{"a", false, "a-fixture"}, {"b", true, "b-fixture"}} {
		key, err := c.localHostSecret(ctx, tc.id, tc.defaultHost)
		if err != nil || key != tc.key {
			t.Fatalf("host %s lost its own key", tc.id)
		}
	}
	a.IsDefault = true
	if _, err := c.SaveLocalHost(ctx, *a); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteLocalHost(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	key, err := c.localHostSecret(ctx, "b", true)
	if err != nil || key != "b-fixture" {
		t.Fatal("deleting the default lost the successor key")
	}
}

func TestNativeAssignmentsAreUniqueAndGroupedByHost(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	for _, h := range []LocalHost{{ID: "b", Name: "B", Endpoint: "http://b.test", NativeModels: []string{"z", "a", "z", " a "}}, {ID: "a", Name: "A", Endpoint: "http://a.test", NativeModels: []string{"q"}, IsDefault: true}} {
		if _, err := c.SaveLocalHost(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	assignments, err := c.NativeModelAssignments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range assignments {
		ids = append(ids, a.HostID+"::"+a.Model)
	}
	if !slices.Equal(ids, []string{"a::q", "b::a", "b::z"}) {
		t.Fatal(ids)
	}
}

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

func TestLocalHostEditPreservesNativeModelAssignments(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	host, err := c.SaveLocalHost(ctx, LocalHost{ID: "gpu", Name: "GPU", Endpoint: "http://localhost:8000", NativeModels: []string{"large"}})
	if err != nil {
		t.Fatal(err)
	}
	host.Name = "Renamed GPU"
	host.NativeModels = nil // older clients do not send this field
	if _, err := c.SaveLocalHost(ctx, *host); err != nil {
		t.Fatal(err)
	}
	assigned, err := c.NativeModelAssignments(ctx)
	if err != nil || len(assigned) != 1 || assigned[0].Model != "large" {
		t.Fatalf("host edit erased assigned model: %+v, %v", assigned, err)
	}
}
