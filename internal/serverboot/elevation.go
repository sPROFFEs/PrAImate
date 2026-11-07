package serverboot

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func elevatedRequest(parent context.Context, c Configuration, action, password string, launch func(string, string) error) error {
	dir, err := os.MkdirTemp(c.Root, ".startup-request-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err = secureDirectory(dir); err != nil {
		return err
	}
	body, _ := json.Marshal(helperRequest{Configuration: c, Action: action, Password: password})
	defer clear(body)
	protected, err := protect(body)
	if err != nil {
		return err
	}
	defer clear(protected)
	request := filepath.Join(dir, "request")
	if err = privateWrite(request, protected); err != nil {
		return err
	}
	if err = launch(c.Binary, request); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("Host authorization did not complete. Approve with the same OS account, then retry")
		case <-tick.C:
			raw, err := os.ReadFile(filepath.Join(dir, "result"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			var result struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(raw, &result) != nil {
				continue
			}
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return nil
		}
	}
}
