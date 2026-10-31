package main

import (
	"context"
	"errors"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

type dashboardCacheEntry struct {
	cachedAt time.Time
	data     *core.UsageDashboard
}

const dashboardCacheTTL = 15 * time.Second

func (a *App) UsageDashboard(month string) (*core.UsageDashboard, error) {
	if a.core == nil {
		return nil, errors.New("unlock the database to view usage")
	}

	a.dashboardMu.Lock()
	if entry, ok := a.dashboardCache[month]; ok && time.Since(entry.cachedAt) < dashboardCacheTTL {
		a.dashboardMu.Unlock()
		return entry.data, nil
	}
	a.dashboardMu.Unlock()

	d, err := a.core.UsageDashboard(context.Background(), month)
	if err != nil {
		return nil, err
	}

	a.dashboardMu.Lock()
	if a.dashboardCache == nil {
		a.dashboardCache = make(map[string]dashboardCacheEntry)
	}
	a.dashboardCache[month] = dashboardCacheEntry{
		cachedAt: time.Now(),
		data:     d,
	}
	a.dashboardMu.Unlock()

	return d, nil
}

// InvalidateUsageDashboard clears the in-memory usage cache so subsequent
// requests query fresh data from the store.
func (a *App) InvalidateUsageDashboard() {
	a.dashboardMu.Lock()
	a.dashboardCache = nil
	a.dashboardMu.Unlock()
}
