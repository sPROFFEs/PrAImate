package studio

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
)

type stagedAttachment struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Image bool   `json:"image"`
}

func stageAttachments(chatID string, sources []string) ([]stagedAttachment, error) {
	if chatID == "" || filepath.Base(chatID) != chatID {
		return nil, errors.New("invalid chat ID")
	}
	if len(sources) > 20 {
		return nil, errors.New("at most 20 attachments can be staged per turn")
	}
	base, err := appdata.Root()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "attachments", chatID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	out := make([]stagedAttachment, 0, len(sources))
	for _, source := range sources {
		info, err := os.Lstat(source)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<20 {
			return nil, fmt.Errorf("attachment %q must be a regular file no larger than 64 MiB", filepath.Base(source))
		}
		name := filepath.Base(source)
		target := filepath.Join(dir, name)
		for n := 2; ; n++ {
			if _, err := os.Stat(target); os.IsNotExist(err) {
				break
			}
			ext := filepath.Ext(name)
			target = filepath.Join(dir, strings.TrimSuffix(name, ext)+fmt.Sprintf("-%d", n)+ext)
		}
		in, err := os.Open(source)
		if err != nil {
			return nil, err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, err = io.Copy(file, in)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
		_ = in.Close()
		if err != nil {
			_ = os.Remove(target)
			return nil, err
		}
		switch strings.ToLower(filepath.Ext(target)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
			out = append(out, stagedAttachment{Name: filepath.Base(target), Path: target, Image: true})
		default:
			out = append(out, stagedAttachment{Name: filepath.Base(target), Path: target})
		}
	}
	return out, nil
}
