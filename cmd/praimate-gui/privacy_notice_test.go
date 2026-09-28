package main

import (
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

func TestPrivacyNoticeRequiredByVersion(t *testing.T) {
	if !privacyNoticeRequired(nil) {
		t.Fatal("new installation must show the privacy notice")
	}
	if !privacyNoticeRequired(&launcher.Config{}) {
		t.Fatal("unacknowledged config must show the privacy notice")
	}
	if privacyNoticeRequired(&launcher.Config{PrivacyNoticeAcceptedVersion: privacyNoticeVersion}) {
		t.Fatal("current notice acknowledgement should suppress the popup")
	}
	if !privacyNoticeRequired(&launcher.Config{PrivacyNoticeAcceptedVersion: "old"}) {
		t.Fatal("old notice acknowledgement must prompt again")
	}
}

func TestAcceptPrivacyNoticePersists(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	app := NewApp()
	before, err := app.PrivacyNotice()
	if err != nil {
		t.Fatal(err)
	}
	if !before.Required {
		t.Fatal("notice should initially be required")
	}
	if err := app.AcceptPrivacyNotice(); err != nil {
		t.Fatal(err)
	}
	after, err := app.PrivacyNotice()
	if err != nil {
		t.Fatal(err)
	}
	if after.Required {
		t.Fatal("notice should be acknowledged")
	}
}
