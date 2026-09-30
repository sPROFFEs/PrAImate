package main

import (
	"context"
	"errors"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func (a *App) UsageDashboard(month string) (*core.UsageDashboard, error) {
	if a.core == nil {
		return nil, errors.New("unlock the database to view usage")
	}
	return a.core.UsageDashboard(context.Background(), month)
}
