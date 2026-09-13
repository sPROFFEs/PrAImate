package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"git.jtsec.local/lab/PrAImate/internal/store"
)

// DatabaseLockInfo is the only backend state the frontend needs before Core
// exists. No DB-backed binding is usable until Unlocked is true.
type DatabaseLockInfo struct {
	Unlocked          bool   `json:"unlocked"`
	SetupRequired     bool   `json:"setupRequired"`
	RememberSupported bool   `json:"rememberSupported"`
	Error             string `json:"error,omitempty"`
}

type DatabaseUnlockResult struct {
	Unlocked bool   `json:"unlocked"`
	Warning  string `json:"warning,omitempty"`
}

func (a *App) DatabaseLockStatus() DatabaseLockInfo {
	info := DatabaseLockInfo{
		Unlocked:          a.core != nil,
		RememberSupported: runtime.GOOS == "windows" || runtime.GOOS == "linux",
		Error:             a.initErr,
	}
	if info.Unlocked || a.dbPath == "" {
		return info
	}
	setup, err := store.PasswordSetupRequired(a.dbPath)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.SetupRequired = setup
	return info
}

// InitializeDatabasePassword creates a password envelope around a new or
// legacy database key. Password confirmation is enforced independently in the
// backend so a forged frontend call cannot bypass it.
func (a *App) InitializeDatabasePassword(password, confirmation string, remember bool) (*DatabaseUnlockResult, error) {
	if password != confirmation {
		return nil, errors.New("database passwords do not match")
	}
	return a.unlockDatabase(password, remember, true)
}

func (a *App) UnlockDatabase(password string, remember bool) (*DatabaseUnlockResult, error) {
	return a.unlockDatabase(password, remember, false)
}

// ForgetDatabasePassword disables automatic unlock without closing the
// currently running process. The password will be required next launch.
func (a *App) ForgetDatabasePassword() error {
	if a.dbPath == "" {
		return errors.New("database path is unavailable")
	}
	return store.ForgetRememberedPassword(a.dbPath)
}

// ChangeDatabasePassword re-wraps the live database key after verifying the
// current password. The database remains open and its encrypted pages are not
// rewritten. Remember controls the OS credential used on the next launch.
func (a *App) ChangeDatabasePassword(currentPassword, newPassword, confirmation string, remember bool) (*DatabaseUnlockResult, error) {
	if newPassword != confirmation {
		return nil, errors.New("new database passwords do not match")
	}

	a.unlockMu.Lock()
	defer a.unlockMu.Unlock()
	if a.st == nil || a.core == nil {
		return nil, errors.New("database is not unlocked")
	}
	if err := a.st.ChangePassword(currentPassword, newPassword); err != nil {
		return nil, err
	}

	result := &DatabaseUnlockResult{Unlocked: true}
	if remember {
		if err := store.RememberPassword(a.dbPath, newPassword); err != nil {
			_ = store.ForgetRememberedPassword(a.dbPath)
			result.Warning = "Database password changed, but this system could not securely remember the new password: " + err.Error()
		}
	} else if err := store.ForgetRememberedPassword(a.dbPath); err != nil {
		result.Warning = "Database password changed, but the old remembered credential could not be removed: " + err.Error()
	}
	return result, nil
}

func (a *App) unlockDatabase(password string, remember, initialize bool) (*DatabaseUnlockResult, error) {
	a.unlockMu.Lock()
	defer a.unlockMu.Unlock()
	if a.core != nil {
		return &DatabaseUnlockResult{Unlocked: true}, nil
	}
	if a.dbPath == "" {
		return nil, errors.New("database path is unavailable")
	}

	var (
		st  *store.Store
		err error
	)
	if initialize {
		st, err = store.InitializeWithPassword(a.dbPath, password)
	} else {
		st, err = store.OpenWithPassword(a.dbPath, password)
	}
	if err != nil {
		return nil, err
	}

	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.initializeUnlockedStore(ctx, st); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("initialise PrAImate: %w", err)
	}
	a.initErr = ""

	result := &DatabaseUnlockResult{Unlocked: true}
	if remember {
		if err := store.RememberPassword(a.dbPath, password); err != nil {
			result.Warning = "Database unlocked, but this system could not securely remember the password: " + err.Error()
		}
	} else {
		// Best effort: unchecking Remember removes an earlier opt-in.
		_ = store.ForgetRememberedPassword(a.dbPath)
	}
	return result, nil
}
