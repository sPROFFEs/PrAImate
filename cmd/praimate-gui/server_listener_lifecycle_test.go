//go:build server

package main

import (
	"context"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/serverboot"
	"strings"
	"testing"
	"time"
)

func TestServerListenerRestartPreservesAnAcceptedRunningOperation(t *testing.T) {
	a := serverSettingsFixture(t)
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	a.edition.registry.Register("api.FixturePending", "control", true, func(ctx context.Context) (string, error) {
		close(entered)
		select {
		case <-finish:
			return "completed", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	principal := remote.Principal{ID: "fixture-admin", Role: "admin"}
	id := fmt.Sprintf("%d_%s", time.Now().UnixMilli(), strings.Repeat("a", 32))
	operations := a.edition.gateway.Operations()
	if _, err := operations.Submit(principal, id, "api.FixturePending", []byte(`[]`)); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := a.ServerListener("restart"); err != nil {
		t.Fatal(err)
	}
	if a.edition.gateway.Operations() != operations {
		t.Fatal("listener restart replaced its durable operation owner")
	}
	current, err := operations.Get(principal, id)
	if err != nil || current.Status != "running" {
		t.Fatalf("accepted operation was interrupted by listener restart: %+v %v", current, err)
	}
	close(finish)
	deadline := time.After(2 * time.Second)
	for {
		current, err = operations.Get(principal, id)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == "completed" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("operation did not complete after listener restart")
		case <-time.After(time.Millisecond):
		}
	}
}
func TestManagedListenerUpdatesItsPrivateConnectionMetadata(t *testing.T) {
	a := serverSettingsFixture(t)
	a.headless = true
	a.edition.localToken = "synthetic-local-owner-credential-for-test"
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	cleanup, err := a.writeManagedSession()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := a.ServerListener("restart"); err != nil {
		t.Fatal(err)
	}
	session, err := serverboot.LoadSession(a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if session.Origin != serverOriginURL(a.edition.address, false) {
		t.Fatal("local GUI metadata retained the previous listener address")
	}
	if err := a.ServerListener("stop"); err != nil {
		t.Fatal(err)
	}
	if a.edition.apiOperations == nil || a.edition.apiDevices == nil {
		t.Fatal("stopping access discarded the profile's API state")
	}
}
