package remote

import (
	"context"
	"sync"
	"time"
)

type Lease struct {
	Terminal string    `json:"terminal"`
	Owner    string    `json:"owner"`
	ClientID string    `json:"clientId,omitempty"`
	Until    time.Time `json:"until"`
}
type Leases struct {
	mu    sync.Mutex
	items map[string]Lease
}

func NewLeases() *Leases { return &Leases{items: map[string]Lease{}} }
func (l *Leases) Claim(ctx context.Context, id string, force bool) (Lease, error) {
	p := Identity(ctx)
	if !p.Allows("control") {
		return Lease{}, Fail(403, "forbidden", "Terminal control requires control permission")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if id == "" {
		return Lease{}, Fail(400, "invalid_terminal", "Terminal ID is required")
	}
	current := l.items[id]
	if time.Now().Before(current.Until) && (current.Owner != p.ID || current.ClientID != p.ClientID) && !(force && (p.Role == "admin" || current.Owner == p.ID)) {
		return Lease{}, Fail(409, "terminal_busy", "Another connection controls this terminal; take control explicitly or wait for its lease")
	}
	for id, item := range l.items {
		if time.Now().After(item.Until) {
			delete(l.items, id)
		}
	}
	lease := Lease{Terminal: id, Owner: p.ID, ClientID: p.ClientID, Until: time.Now().UTC().Add(45 * time.Second)}
	l.items[id] = lease
	return lease, nil
}
func (l *Leases) Release(id string) { l.mu.Lock(); delete(l.items, id); l.mu.Unlock() }
func (l *Leases) TakeLocal(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items[id] = Lease{Terminal: id, Owner: "desktop", Until: time.Now().UTC().Add(45 * time.Second)}
}
func (l *Leases) RemoteControls(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.items[id].Owner != "desktop" && time.Now().Before(l.items[id].Until)
}
