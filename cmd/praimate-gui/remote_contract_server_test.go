//go:build server

package main

import (
	"context"
	"errors"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRemoteControlCatalogCanSelectARouteWithoutAdminConfigurationAccess(t *testing.T) {
	a := NewApp()
	r := a.remoteMethods()
	controller := remote.Principal{ID: "controller", Role: "control"}
	available := map[string]bool{}
	for _, method := range r.Catalog(controller) {
		available[method.Name] = true
	}
	for _, method := range []string{"api.ListCLIs", "api.ListCLIModels", "api.PreflightExecution", "api.StartCleanChat", "api.WriteTerminal"} {
		if !available[method] {
			t.Errorf("controller cannot use %s", method)
		}
	}
	if available["api.GetGUISetting"] || available["api.SaveLocalHost"] || available["api.CloudflareStatus"] || available["api.SaveCloudflareConfig"] || available["api.CloudflareService"] || available["api.ServerPairing"] {
		t.Fatal("controller can administer stored configuration")
	}
	_, err := r.Call(remote.WithPrincipal(context.Background(), controller), "api.GetGUISetting", []byte(`["example"]`))
	if err == nil {
		t.Fatal("catalog filtering was not enforced at execution")
	}
}

func TestRemoteCatalogCoversFrontendDataBindings(t *testing.T) {
	a := NewApp()
	catalog := a.remoteMethods()
	api, err := os.ReadFile("frontend/src/lib/api.js")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := os.ReadFile("frontend/src/lib/remoteBridge.js")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`call\('([A-Za-z0-9_]+)'`)
	for _, match := range pattern.FindAllStringSubmatch(string(api), -1) {
		name := match[1]
		if _, ok := catalog.Lookup("api." + name); ok {
			continue
		}
		if strings.Contains(string(bridge), name+":") {
			continue
		}
		t.Errorf("Frontend binding %s has no remote route or client adapter", name)
	}
}
func TestRemoteErrorsRetainDiagnosticsWithoutCommonCredentials(t *testing.T) {
	a := &App{}
	err := a.remoteError(errors.New("TLS failed at https://user:private@host.local/v1?api_key=example-secret Authorization: Bearer example-credential"))
	for _, secret := range []string{"private", "example-secret", "example-credential"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("credential retained in remote error")
		}
	}
	if !strings.Contains(err.Error(), "TLS failed") {
		t.Fatal("useful error diagnostics erased")
	}
	var public *remote.APIError
	if !errors.As(err, &public) {
		t.Fatal("unstructured remote error")
	}
}
