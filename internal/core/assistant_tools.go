package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
)

// ExecuteApplicationTool reuses the managed command/filesystem/network broker.
// The Assistant's independent capability check and approval happen upstream;
// this method exposes only the specific capability needed by the action.
func (c *Core) ExecuteApplicationTool(ctx context.Context, cwd, tool string, args map[string]any) (string, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	appRoot, err := appdata.Root()
	if err != nil {
		return "", err
	}
	appRoot, _ = filepath.Abs(appRoot)
	if resolved, err := filepath.EvalSymlinks(appRoot); err == nil {
		appRoot = resolved
	}
	if rel, err := filepath.Rel(appRoot, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("Assistant system/project tools cannot target PrAImate's internal state; use application actions")
	}
	caps := AgentCapabilities{}
	switch tool {
	case "command.run":
		caps.ExecuteCommands = true
	case "network.get":
		caps.Network = true
	case "project.read", "project.list", "project.search":
		caps.ReadProject = true
	case "project.write":
		caps.ModifyFiles = true
	default:
		return "", errors.New("unsupported Assistant system tool")
	}
	broker, err := newManagedToolBroker(ctx, nil, caps, root, &ApprovalConfig{Request: func(context.Context, string, map[string]any) (bool, error) { return true, nil }}, nil)
	if err != nil {
		return "", err
	}
	defer broker.Close()
	if strings.HasPrefix(tool, "project.") {
		target, err := broker.resolveProjectPath(textToolArg(args, "path"), tool == "project.write")
		if err != nil {
			return "", err
		}
		if rel, err := filepath.Rel(appRoot, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", errors.New("application storage is not a project file")
		}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return broker.ExecuteTool(ctx, tool, raw)
}
func textToolArg(args map[string]any, key string) string { s, _ := args[key].(string); return s }
