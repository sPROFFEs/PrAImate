package remote

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

type memoryState struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (m *memoryState) Load(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.values[key]...), nil
}
func (m *memoryState) Save(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	m.values[key] = append([]byte(nil), value...)
	return nil
}
func TestDevicePairingPersistenceAndRevocation(t *testing.T) {
	state := &memoryState{}
	devices, err := NewDevices(state)
	if err != nil {
		t.Fatal(err)
	}
	code, err := devices.CreatePairing("phone", "control")
	if err != nil {
		t.Fatal(err)
	}
	device, token, err := devices.Pair(code.Code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(state.values["devices"], []byte(token)) {
		t.Fatal("raw device token stored instead of a hash")
	}
	if _, _, err := devices.Pair(code.Code, "second phone"); err == nil {
		t.Fatal("pairing code reused")
	}
	restored, err := NewDevices(state)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := restored.Authenticate(token)
	if !ok || identity.ID != device.ID || identity.Role != "control" {
		t.Fatal("device did not survive restart")
	}
	if err := restored.Revoke(device.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := restored.Authenticate(token); ok {
		t.Fatal("revoked token accepted")
	}
	again, err := NewDevices(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Authenticate(token); ok {
		t.Fatal("revocation did not survive restart")
	}
}
