package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type attachmentFlags []string

func (v *attachmentFlags) String() string { return strings.Join(*v, ", ") }
func (v *attachmentFlags) Set(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("--attach requires a file path")
	}
	*v = append(*v, path)
	return nil
}

func queueAttachment(paths []string, root, path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if len(path) >= 2 && ((path[0] == '"' && path[len(path)-1] == '"') || (path[0] == '\'' && path[len(path)-1] == '\'')) {
		path = path[1 : len(path)-1]
	}
	if path == "" {
		return paths, errors.New("usage: /attach PATH (one file, quotes optional)")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return paths, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return paths, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return paths, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<20 {
		return paths, errors.New("attachments must be regular files under 64 MiB (images: 8 MiB each)")
	}
	for _, existing := range paths {
		if existing == path {
			return paths, nil
		}
	}
	if len(paths) >= 20 {
		return paths, errors.New("at most 20 attachments per message")
	}
	return append(paths, path), nil
}

func detachAttachment(paths []string, arg string) ([]string, error) {
	if arg == "all" {
		return nil, nil
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(paths) {
		return paths, fmt.Errorf("use /detach N (1..%d) or /detach all", len(paths))
	}
	return append(paths[:n-1], paths[n:]...), nil
}
