package main

import (
	"runtime"

	"git.jtsec.local/lab/PrAImate/internal/launcher"
)

const privacyNoticeVersion = "5"

// PrivacyNoticeInfo is the first-run disclosure rendered by the desktop app.
type PrivacyNoticeInfo struct {
	Required bool   `json:"required"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

func privacyNoticeRequired(cfg *launcher.Config) bool {
	return cfg == nil || cfg.PrivacyNoticeAcceptedVersion != privacyNoticeVersion
}

func (a *App) PrivacyNotice() (*PrivacyNoticeInfo, error) {
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return nil, err
	}
	return &PrivacyNoticeInfo{
		Required: privacyNoticeRequired(cfg),
		Version:  privacyNoticeVersion,
		Platform: runtime.GOOS,
	}, nil
}

func (a *App) AcceptPrivacyNotice() error {
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &launcher.Config{}
	}
	cfg.PrivacyNoticeAcceptedVersion = privacyNoticeVersion
	return launcher.SaveConfig(cfg)
}
