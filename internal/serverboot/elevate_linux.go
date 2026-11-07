//go:build linux

package serverboot

import (
	"context"
	"errors"
)

func elevate(context.Context, Configuration, string, string) error {
	return errors.New("Linux startup uses the per-user systemd manager")
}
func authorizedControl(helperRequest) error {
	return errors.New("No elevated PrAImate helper is used on Linux")
}
