package remote

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRegistryAuthorizationAndTypedArguments(t *testing.T) {
	r := NewRegistry()
	calls := 0
	r.Register("change", "admin", true, func(value struct {
		Name string `json:"name"`
	}) (string, error) {
		calls++
		return value.Name, nil
	})
	viewer := WithPrincipal(context.Background(), Principal{ID: "viewer", Role: "viewer"})
	if _, err := r.Call(viewer, "change", json.RawMessage(`[{"name":"test"}]`)); err == nil {
		t.Fatal("viewer mutated state")
	}
	admin := WithPrincipal(context.Background(), Principal{ID: "owner", Role: "admin"})
	if _, err := r.Call(admin, "change", json.RawMessage(`[{"name":"test","unexpected":true}]`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := r.Call(admin, "privateMethod", nil); err == nil {
		t.Fatal("unregistered method resolved")
	}
	result, err := r.Call(admin, "change", json.RawMessage(`[{"name":"test"}]`))
	if err != nil || result != "test" || calls != 1 {
		t.Fatalf("result=%v, calls=%d, error=%v", result, calls, err)
	}
}
