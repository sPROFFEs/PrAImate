package store

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
)

// DefaultDBPath returns the canonical PrAImate database location for
// the current OS. Callers should pass this to Open() unless the user
// has overridden it.
//
// Layout:
//
//	Linux:   $XDG_CONFIG_HOME/praimate/db.sqlite (or ~/.config/praimate/...)
//	Windows: %APPDATA%/praimate/db.sqlite
func DefaultDBPath() (string, error) {
	if env := os.Getenv("PRAIMATE_DB"); env != "" {
		return env, nil
	}
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "db.sqlite")
	if err := migrateLegacyDatabase(path); err != nil {
		return "", err
	}
	return path, nil
}

func migrateLegacyDatabase(target string) error {
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, root := range appdata.LegacyRoots() {
		source := filepath.Join(root, "db.sqlite")
		if _, err := os.Stat(source); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		// Move the database last. Its presence is the migration-complete
		// marker; if any earlier rename fails, the next launch can retry
		// without opening the encrypted DB against a newly generated key.
		for _, suffix := range []string{".key", "-wal", "-shm", ""} {
			oldPath := source + suffix
			newPath := target + suffix
			if _, err := os.Stat(oldPath); err != nil {
				continue
			}
			if err := os.Rename(oldPath, newPath); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}
