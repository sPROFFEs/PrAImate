package main

import (
	"runtime"

	"github.com/sPROFFEs/PrAImate/internal/version"
)

// AboutInfo contains runtime facts; the privacy and security explanations
// themselves live in the frontend so the first-run notice and About page can
// share the same wording.
type AboutInfo struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	DBPath            string `json:"dbPath,omitempty"`
	DBKeyPath         string `json:"dbKeyPath,omitempty"`
	DatabaseEncrypted bool   `json:"databaseEncrypted"`
	DatabaseCipher    string `json:"databaseCipher"`
}

func (a *App) About() AboutInfo {
	info := AboutInfo{
		Name:              version.Name,
		Version:           version.Current,
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		DatabaseEncrypted: a.st != nil,
		DatabaseCipher:    "AES-256-XTS",
	}
	if a.st != nil {
		info.DBPath = a.st.Path()
		info.DBKeyPath = a.st.EncryptionKeyPath()
	}
	return info
}
