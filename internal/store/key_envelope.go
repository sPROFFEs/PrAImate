package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf8"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	keyEnvelopeVersion = 1
	keyEnvelopeKDF     = "argon2id"
	keyEnvelopeCipher  = "xchacha20-poly1305"

	defaultArgonTime    = uint32(3)
	defaultArgonMemory  = uint32(64 * 1024) // 64 MiB, expressed in KiB.
	defaultArgonThreads = uint8(4)

	rememberService = "PrAImate"
)

var (
	// ErrPasswordRequired means a password envelope exists but no remembered
	// credential was available. It is an expected locked-startup state.
	ErrPasswordRequired = errors.New("database password required")
	// ErrPasswordSetupRequired means this installation still has a legacy raw
	// key, or no database key at all. The GUI must collect and confirm a new
	// password before opening the database.
	ErrPasswordSetupRequired = errors.New("database password setup required")
	// ErrInvalidPassword intentionally does not distinguish a wrong password
	// from envelope authentication failure.
	ErrInvalidPassword = errors.New("unable to unlock database")
)

type keyEnvelope struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Time       uint32 `json:"time"`
	MemoryKiB  uint32 `json:"memory_kib"`
	Threads    uint8  `json:"threads"`
	Salt       string `json:"salt"`
	Cipher     string `json:"cipher"`
	Nonce      string `json:"nonce"`
	WrappedKey string `json:"wrapped_key"`
}

type kdfParams struct {
	time      uint32
	memoryKiB uint32
	threads   uint8
}

var productionKDF = kdfParams{
	time:      defaultArgonTime,
	memoryKiB: defaultArgonMemory,
	threads:   defaultArgonThreads,
}

// PasswordSetupRequired reports whether the database needs a password to be
// created around a fresh or legacy raw key.
func PasswordSetupRequired(path string) (bool, error) {
	raw, err := os.ReadFile(KeyPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if len(raw) == encryptionKeyBytes {
		return true, nil
	}
	var envelope keyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false, fmt.Errorf("invalid database key envelope: %w", err)
	}
	if err := validateEnvelope(envelope); err != nil {
		return false, err
	}
	return false, nil
}

// InitializeWithPassword creates the first random database key, or wraps a
// legacy raw key, then opens the database. Existing encrypted databases are
// verified with their legacy key before the raw key is replaced.
func InitializeWithPassword(path, password string) (*Store, error) {
	return initializeWithPassword(path, password, productionKDF)
}

func initializeWithPassword(path, password string, params kdfParams) (*Store, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	keyPath := KeyPath(path)
	raw, err := os.ReadFile(keyPath)
	switch {
	case err == nil && len(raw) != encryptionKeyBytes:
		return nil, errors.New("database password is already configured")
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read database key: %w", err)
	}

	var key []byte
	if len(raw) == encryptionKeyBytes {
		key = append([]byte(nil), raw...)
		if err := verifyExistingEncryptedDatabase(path, key); err != nil {
			zeroBytes(key)
			return nil, err
		}
	} else {
		if err := refuseMissingKeyForEncryptedDatabase(path); err != nil {
			return nil, err
		}
		key = make([]byte, encryptionKeyBytes)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("generate database key: %w", err)
		}
	}

	envelope, err := sealKeyEnvelope(key, password, params)
	if err != nil {
		zeroBytes(key)
		return nil, err
	}
	if err := replaceKeyFile(keyPath, envelope); err != nil {
		zeroBytes(key)
		return nil, err
	}
	return openWithKey(path, key, []byte(password))
}

// OpenWithPassword unlocks an existing password envelope and opens the
// encrypted database.
func OpenWithPassword(path, password string) (*Store, error) {
	key, err := unlockEnvelopeFile(KeyPath(path), password)
	if err != nil {
		return nil, err
	}
	return openWithKey(path, key, []byte(password))
}

// ChangePassword replaces only the password-protected envelope around the
// live database key. The encrypted database itself is not rewritten. The
// current password is verified against both the on-disk envelope and the key
// held by this Store before the replacement is committed.
func (s *Store) ChangePassword(currentPassword, newPassword string) error {
	return s.changePassword(currentPassword, newPassword, productionKDF)
}

func (s *Store) changePassword(currentPassword, newPassword string, params kdfParams) error {
	if s == nil {
		return errors.New("store.ChangePassword: nil store")
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	if s.db == nil || len(s.key) != encryptionKeyBytes {
		return errors.New("store.ChangePassword: store is closed")
	}

	verifiedKey, err := unlockEnvelopeFile(s.keyPath, currentPassword)
	if err != nil {
		return err
	}
	defer zeroBytes(verifiedKey)
	if subtle.ConstantTimeCompare(verifiedKey, s.key) != 1 {
		return ErrInvalidPassword
	}

	envelope, err := sealKeyEnvelope(s.key, newPassword, params)
	if err != nil {
		return err
	}
	if err := replaceKeyFile(s.keyPath, envelope); err != nil {
		return fmt.Errorf("change database password: %w", err)
	}

	zeroBytes(s.password)
	s.password = []byte(newPassword)
	return nil
}

// Open uses an explicitly remembered password. It never creates a raw key and
// never silently falls back when the operating-system credential store is
// absent or locked.
func Open(path string) (*Store, error) {
	setup, err := PasswordSetupRequired(path)
	if err != nil {
		return nil, err
	}
	if setup {
		return nil, ErrPasswordSetupRequired
	}
	password, err := RecalledPassword(path)
	if err != nil {
		return nil, ErrPasswordRequired
	}
	st, openErr := OpenWithPassword(path, password)
	zeroString(&password)
	if openErr != nil {
		return nil, openErr
	}
	return st, nil
}

// RememberPassword stores the database password in the OS credential store.
// Windows uses Credential Manager/DPAPI; Linux uses Secret Service. Failure is
// explicit so callers can keep the database unlocked without claiming that
// the opt-in succeeded.
func RememberPassword(path, password string) error {
	if password == "" {
		return errors.New("empty database password")
	}
	return keyring.Set(rememberService, rememberAccount(path), password)
}

func RecalledPassword(path string) (string, error) {
	return keyring.Get(rememberService, rememberAccount(path))
}

func ForgetRememberedPassword(path string) error {
	err := keyring.Delete(rememberService, rememberAccount(path))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func rememberAccount(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	sum := sha256.Sum256([]byte(runtime.GOOS + "\x00" + abs))
	return fmt.Sprintf("database-%x", sum[:12])
}

func sealKeyEnvelope(key []byte, password string, params kdfParams) ([]byte, error) {
	if len(key) != encryptionKeyBytes {
		return nil, fmt.Errorf("database key has %d bytes; expected %d", len(key), encryptionKeyBytes)
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generate envelope salt: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate envelope nonce: %w", err)
	}
	envelope := keyEnvelope{
		Version:   keyEnvelopeVersion,
		KDF:       keyEnvelopeKDF,
		Time:      params.time,
		MemoryKiB: params.memoryKiB,
		Threads:   params.threads,
		Salt:      base64.RawStdEncoding.EncodeToString(salt),
		Cipher:    keyEnvelopeCipher,
		Nonce:     base64.RawStdEncoding.EncodeToString(nonce),
	}
	kek := argon2.IDKey([]byte(password), salt, params.time, params.memoryKiB, params.threads, chacha20poly1305.KeySize)
	defer zeroBytes(kek)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, err
	}
	sealed := aead.Seal(nil, nonce, key, envelopeAAD(envelope))
	envelope.WrappedKey = base64.RawStdEncoding.EncodeToString(sealed)
	body, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

func unlockEnvelopeFile(path, password string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrPasswordSetupRequired
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == encryptionKeyBytes {
		return nil, ErrPasswordSetupRequired
	}
	return unlockEnvelope(raw, password)
}

func unlockEnvelope(raw []byte, password string) ([]byte, error) {
	var envelope keyEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("invalid database key envelope: %w", err)
	}
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(envelope.Salt)
	if err != nil || len(salt) != 16 {
		return nil, errors.New("invalid database key envelope salt")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, errors.New("invalid database key envelope nonce")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(envelope.WrappedKey)
	if err != nil {
		return nil, errors.New("invalid database key envelope payload")
	}
	kek := argon2.IDKey([]byte(password), salt, envelope.Time, envelope.MemoryKiB, envelope.Threads, chacha20poly1305.KeySize)
	defer zeroBytes(kek)
	aead, err := chacha20poly1305.NewX(kek)
	if err != nil {
		return nil, err
	}
	key, err := aead.Open(nil, nonce, sealed, envelopeAAD(envelope))
	if err != nil || len(key) != encryptionKeyBytes {
		zeroBytes(key)
		return nil, ErrInvalidPassword
	}
	return key, nil
}

func validateEnvelope(envelope keyEnvelope) error {
	if envelope.Version != keyEnvelopeVersion ||
		envelope.KDF != keyEnvelopeKDF ||
		envelope.Cipher != keyEnvelopeCipher {
		return errors.New("unsupported database key envelope")
	}
	// Bounds prevent a corrupt or hostile backup from forcing an excessive
	// Argon2 allocation or CPU loop before authentication can run.
	if envelope.Time < 1 || envelope.Time > 10 ||
		envelope.MemoryKiB < 8*1024 || envelope.MemoryKiB > 256*1024 ||
		envelope.Threads < 1 || envelope.Threads > 32 {
		return errors.New("unsafe database key envelope parameters")
	}
	return nil
}

func envelopeAAD(envelope keyEnvelope) []byte {
	return []byte(fmt.Sprintf(
		"praimate-db-key\x00%d\x00%s\x00%d\x00%d\x00%d\x00%s\x00%s",
		envelope.Version, envelope.KDF, envelope.Time, envelope.MemoryKiB,
		envelope.Threads, envelope.Salt, envelope.Cipher,
	))
}

func validatePassword(password string) error {
	if !utf8.ValidString(password) {
		return errors.New("database password is not valid UTF-8")
	}
	if utf8.RuneCountInString(password) < 12 {
		return errors.New("database password must contain at least 12 characters")
	}
	return nil
}

func replaceKeyFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".new"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(body); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	old := path + ".replacing"
	_ = os.Remove(old)
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, old); err != nil {
			return fmt.Errorf("stage old database key: %w", err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(old, path)
		return fmt.Errorf("install database key envelope: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove legacy database key: %w", err)
	}
	ok = true
	return nil
}

func refuseMissingKeyForEncryptedDatabase(path string) error {
	header, err := readDatabaseHeader(path)
	if errors.Is(err, os.ErrNotExist) || len(header) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if len(header) == len(sqliteHeader) && string(header) == string(sqliteHeader) {
		return nil
	}
	return errors.New("encrypted database key is missing; restore its key envelope or reset PrAImate data")
}

func verifyExistingEncryptedDatabase(path string, key []byte) error {
	header, err := readDatabaseHeader(path)
	if errors.Is(err, os.ErrNotExist) || len(header) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if len(header) == len(sqliteHeader) && string(header) == string(sqliteHeader) {
		return nil
	}
	db, err := openSQLDatabase(path, key, true)
	if err != nil {
		return fmt.Errorf("verify legacy database key: %w", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&count); err != nil {
		return fmt.Errorf("verify legacy database key: %w", err)
	}
	return nil
}

func readDatabaseHeader(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	header := make([]byte, len(sqliteHeader))
	n, err := io.ReadFull(f, header)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return header[:n], nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func zeroString(value *string) {
	if value == nil {
		return
	}
	// Strings are immutable in Go, so this only releases our reference. The
	// Store keeps passwords as mutable byte slices for best-effort clearing.
	*value = ""
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
