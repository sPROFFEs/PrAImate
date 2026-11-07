//go:build server

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"github.com/sPROFFEs/PrAImate/internal/appdata"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) registerRemoteTransfers(r *remote.Registry) {
	r.Register("agents.requirements_script", "admin", true, a.setRequirementsScriptFile)
	r.Register("agents.knowledge_add", "admin", true, func(id string, paths []string) ([]string, error) {
		if _, err := core.AddAgentKnowledgeFiles(id, paths); err != nil {
			return nil, err
		}
		return core.ListAgentKnowledge(id)
	})
	r.Register("uploads.put", "admin", true, func(name, data string) (any, error) {
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00") {
			return ChatAttachment{}, errors.New("invalid upload filename")
		}
		body, err := base64.StdEncoding.DecodeString(data)
		if err != nil || len(body) > 8<<20 {
			return ChatAttachment{}, errors.New("invalid upload or file exceeds 8 MiB")
		}
		return a.storeRemoteUpload(a.ctx, name, bytes.NewReader(body))
	})
	r.Register("agents.download", "admin", false, func(id string, pack bool, format string) (map[string]string, error) {
		directory, err := a.remoteExportDirectory()
		if err != nil {
			return nil, err
		}
		ext := ".yaml"
		if format == "markdown" {
			ext = ".md"
		}
		if pack {
			ext = core.AgentPackExt
		}
		path := filepath.Join(directory, "agent"+ext)
		if pack {
			err = a.core.ExportAgentPack(a.ctx, id, path)
		} else {
			err = a.core.ExportAgent(a.ctx, id, path)
		}
		if err != nil {
			return nil, err
		}
		return map[string]string{"name": filepath.Base(id) + ext, "workspace": directory, "path": filepath.Base(path)}, nil
	})
	r.Register("skills.workspace", "admin", true, a.prepareSkillWorkspace)
	r.Register("skills.create", "admin", true, a.prepareNewSkillWorkspace)
	r.Register("agents.knowledge_folder", "admin", false, core.AgentKnowledgeDir)
}

func (a *App) storeRemoteUpload(ctx context.Context, name string, body io.Reader) (any, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00") {
		return nil, errors.New("invalid upload filename")
	}
	root, err := attachmentsRoot()
	if err != nil {
		return nil, err
	}
	root = filepath.Join(root, "remote")
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(root, "upload-")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	_, err = io.Copy(file, body)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		os.RemoveAll(directory)
		return nil, err
	}
	return ChatAttachment{Name: name, Path: path, Image: isImagePath(name)}, nil
}
func (a *App) remoteExportDirectory() (string, error) {
	root, err := appdata.Root()
	if err != nil {
		return "", err
	}
	root = filepath.Join(root, "remote-exports")
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp(root, "export-")
	if err != nil {
		return "", err
	}
	go func() {
		timer := time.NewTimer(30 * time.Minute)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-a.ctx.Done():
		}
		_ = os.RemoveAll(directory)
	}()
	return directory, nil
}
