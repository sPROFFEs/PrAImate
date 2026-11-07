package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationReconnectDoesNotRepeatExecution(t *testing.T) {
	var calls atomic.Int32
	unblock := make(chan struct{})
	done := make(chan struct{})
	o, err := NewOperations(context.Background(), &memoryState{}, func(context.Context, string, json.RawMessage) (any, error) {
		calls.Add(1)
		<-unblock
		close(done)
		return "saved", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{ID: "phone", Role: "admin"}
	id := fmt.Sprintf("%d_request123", time.Now().UnixMilli())
	for i := 0; i < 3; i++ {
		if _, err := o.Submit(p, id, "change", json.RawMessage(`["same"]`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := o.Submit(p, id, "change", json.RawMessage(`["different"]`)); err == nil {
		t.Fatal("ID reuse changed the request")
	}
	close(unblock)
	<-done
	deadline := time.Now().Add(time.Second)
	for {
		op, err := o.Get(p, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("operation did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("executed %d times", calls.Load())
	}
}
func TestTerminalControlHasOneRemoteOwner(t *testing.T) {
	l := NewLeases()
	a := WithPrincipal(context.Background(), Principal{ID: "a", Role: "control"})
	b := WithPrincipal(context.Background(), Principal{ID: "b", Role: "control"})
	if _, err := l.Claim(a, "term", false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Claim(b, "term", true); err == nil {
		t.Fatal("control role stole another device's terminal")
	}
	l.Release("term")
	if _, err := l.Claim(b, "term", false); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalTabsAndDesktopDoNotFightForControl(t *testing.T) {
	leases := NewLeases()
	first := WithPrincipal(context.Background(), Principal{ID: "phone", Role: "control", ClientID: "tab1"})
	second := WithPrincipal(context.Background(), Principal{ID: "phone", Role: "control", ClientID: "tab2"})
	if _, err := leases.Claim(first, "term", false); err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Claim(second, "term", false); err == nil {
		t.Fatal("second tab silently took control")
	}
	if _, err := leases.Claim(second, "term", true); err != nil {
		t.Fatal("device could not explicitly take control of its other tab")
	}
	leases.TakeLocal("term")
	if leases.RemoteControls("term") {
		t.Fatal("desktop resize blocked by its own local controller")
	}
	if _, err := leases.Claim(second, "term", false); err == nil {
		t.Fatal("remote input silently took control from desktop")
	}
}
