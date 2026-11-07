//go:build server

package main

import (
	"errors"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var remoteSecretAssignment = regexp.MustCompile(`(?i)(api[_-]?key|authorization|password|access[_-]?token|secret)["']?\s*[:=]\s*["']?[^\s,"';}]+`)
var remoteURL = regexp.MustCompile(`https?://[^\s<>"']+`)
var remoteAuthorization = regexp.MustCompile(`(?i)(Bearer|Basic)\s+[A-Za-z0-9_./+=-]+`)

func (a *App) remoteError(err error) error {
	if err == nil {
		return nil
	}
	var safe *remote.APIError
	if errors.As(err, &safe) {
		return err
	}
	message := err.Error()
	message = remoteAuthorization.ReplaceAllString(message, "$1 [redacted]")
	message = remoteURL.ReplaceAllStringFunc(message, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[endpoint]"
		}
		u.User = nil
		query := u.Query()
		for key := range query {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "token") || strings.Contains(lower, "key") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") {
				query.Set(key, "[redacted]")
			}
		}
		u.RawQuery = query.Encode()
		return u.String()
	})
	message = remoteSecretAssignment.ReplaceAllString(message, "$1=[redacted]")
	if a.core != nil {
		message, _ = a.core.PrivacyScanner().Redact(message)
	}
	if len(message) > 2048 {
		message = message[:2048]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return remote.Fail(422, "backend_error", message)
}
