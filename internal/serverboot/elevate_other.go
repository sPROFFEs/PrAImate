//go:build !linux && !windows && !darwin

package serverboot

import (
	"context"
	"errors"
)

func elevate(context.Context, Configuration, string, string) error {
	return errors.New("Unsupported service platform")
}
func authorizedControl(helperRequest) error { return errors.New("Unsupported service platform") }
