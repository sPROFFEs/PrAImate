package main

import (
	"context"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type eventOwnerKey struct{}

// Background writers receive the owner's context rather than a Wails runtime.
// The headless owner supplies a no-op local emitter and still publishes remotely.
func appEvent(ctx context.Context, name string, args ...any) {
	if ctx != nil {
		if a, ok := ctx.Value(eventOwnerKey{}).(*App); ok {
			var payload any
			if len(args) == 1 {
				payload = args[0]
			} else {
				payload = args
			}
			a.emitUIEvent(name, payload)
			return
		}
	}
	wruntime.EventsEmit(ctx, name, args...)
}
