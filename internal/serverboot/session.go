package serverboot

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
)

// Session is a private local IPC capability, never a URL parameter or a frontend
// response. The OS account already owns the unlocked service and its profile.
type Session struct {
	Origin  string `json:"origin"`
	Token   string `json:"token"`
	TLSCert string `json:"tlsCert,omitempty"`
	PID     int    `json:"pid"`
}

func (s Session) Validate() error {
	u, err := url.Parse(s.Origin)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Port() == "" || (u.Scheme != "http" && u.Scheme != "https") || len(s.Token) < 32 {
		return errors.New("Invalid local Server session")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || ip.IsUnspecified() {
		return errors.New("Local Server address must belong to this host")
	}
	if ip.IsLoopback() {
		return nil
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	for _, address := range addresses {
		local, _, err := net.ParseCIDR(address.String())
		if err == nil && local.Equal(ip) {
			return nil
		}
	}
	return errors.New("Local Server address does not belong to this host")
}
func SaveSession(database string, s Session) error {
	if err := s.Validate(); err != nil {
		return err
	}
	body, _ := json.Marshal(s)
	return privateWrite(filepath.Join(database+".startup", "session.json"), body)
}
func LoadSession(database string) (Session, error) {
	var s Session
	body, err := readPrivate(filepath.Join(database+".startup", "session.json"), 16384)
	if err != nil {
		return s, err
	}
	defer clear(body)
	if err = json.Unmarshal(body, &s); err != nil {
		return s, errors.New("Invalid local Server session")
	}
	return s, s.Validate()
}
func RemoveSession(database, token string) {
	s, err := LoadSession(database)
	if err == nil && s.Token == token {
		_ = os.Remove(filepath.Join(database+".startup", "session.json"))
	}
}
