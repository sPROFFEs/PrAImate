package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

// State is supplied by the owner. Production stores values in its encrypted
// SQLite database; the transport never opens another profile or key file.
type State interface {
	Load(context.Context, string) ([]byte, error)
	Save(context.Context, string, []byte) error
}
type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Revoked   bool      `json:"revoked"`
}
type deviceSecret struct {
	Device
	Hash string `json:"hash"`
}
type Pairing struct {
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Devices struct {
	mu            sync.Mutex
	state         State
	devices       []deviceSecret
	codes         map[string]Pairing
	attempts      int
	attemptWindow time.Time
}

func randomID(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func NewDevices(state State) (*Devices, error) {
	d := &Devices{state: state, codes: map[string]Pairing{}}
	if state != nil {
		raw, err := state.Load(context.Background(), "devices")
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &d.devices); err != nil {
				return nil, err
			}
		}
	}
	return d, nil
}
func (d *Devices) save() error {
	if d.state == nil {
		return nil
	}
	raw, err := json.Marshal(d.devices)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.state.Save(ctx, "devices", raw)
}
func (d *Devices) CreatePairing(name, role string) (Pairing, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if role != "viewer" && role != "control" && role != "admin" {
		return Pairing{}, Fail(400, "invalid_role", "Use viewer, control or admin")
	}
	for code, p := range d.codes {
		if time.Now().After(p.ExpiresAt) {
			delete(d.codes, code)
		}
	}
	if len(d.codes) >= 16 {
		return Pairing{}, Fail(429, "pairing_limit", "Too many pending pairing codes")
	}
	number, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		return Pairing{}, err
	}
	code := fmt.Sprintf("%08d", number.Int64())
	for {
		if _, ok := d.codes[code]; !ok {
			break
		}
		number, err = rand.Int(rand.Reader, big.NewInt(100000000))
		if err != nil {
			return Pairing{}, err
		}
		code = fmt.Sprintf("%08d", number.Int64())
	}
	p := Pairing{Code: code, Name: name, Role: role, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	d.codes[code] = p
	return p, nil
}
func (d *Devices) Pair(code, name string) (Device, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.attemptWindow) > 5*time.Minute {
		d.attemptWindow = time.Now()
		d.attempts = 0
	}
	d.attempts++
	if d.attempts > 20 {
		return Device{}, "", Fail(429, "pairing_rate_limit", "Too many pairing attempts; retry later")
	}
	p, ok := d.codes[strings.TrimSpace(code)]
	if !ok || time.Now().After(p.ExpiresAt) {
		return Device{}, "", Fail(401, "invalid_pairing", "Invalid or expired pairing code")
	}
	if len(d.devices) >= 256 {
		return Device{}, "", Fail(409, "device_limit", "Device limit reached")
	}
	id, err := randomID(12)
	if err != nil {
		return Device{}, "", err
	}
	secret, err := randomID(32)
	if err != nil {
		return Device{}, "", err
	}
	token := id + "." + secret
	hash := sha256.Sum256([]byte(token))
	if strings.TrimSpace(name) == "" {
		name = p.Name
	}
	if len(name) > 128 {
		return Device{}, "", Fail(400, "invalid_name", "Device name exceeds 128 characters")
	}
	device := Device{ID: id, Name: name, Role: p.Role, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(90 * 24 * time.Hour)}
	d.devices = append(d.devices, deviceSecret{device, hex.EncodeToString(hash[:])})
	if err := d.save(); err != nil {
		d.devices = d.devices[:len(d.devices)-1]
		return Device{}, "", err
	}
	delete(d.codes, p.Code)
	return device, token, nil
}
func (d *Devices) Authenticate(token string) (Principal, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id, _, ok := strings.Cut(token, ".")
	if !ok {
		return Principal{}, false
	}
	hash := sha256.Sum256([]byte(token))
	for _, device := range d.devices {
		if device.ID == id && !device.Revoked && time.Now().Before(device.ExpiresAt) {
			expected, err := hex.DecodeString(device.Hash)
			if err == nil && subtle.ConstantTimeCompare(hash[:], expected) == 1 {
				return Principal{ID: device.ID, Role: device.Role}, true
			}
		}
	}
	return Principal{}, false
}
func (d *Devices) List() []Device {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []Device{}
	for _, item := range d.devices {
		out = append(out, item.Device)
	}
	return out
}
func (d *Devices) Revoke(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.devices {
		if d.devices[i].ID == id {
			old := d.devices[i].Revoked
			d.devices[i].Revoked = true
			if err := d.save(); err != nil {
				d.devices[i].Revoked = old
				return err
			}
			return nil
		}
	}
	return errors.New("device not found")
}
