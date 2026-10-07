package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Upstream `run --format json` omits retry statuses. A launch-scoped plugin
// mirrors the parent session's status without changing project configuration,
// making HTTP requests or depending on a patched third-party binary.
const openCodeStatusPlugin = `export const PrAImateStatus = async () => {
  let root = process.env.PRAIMATE_STATUS_SESSION || '';
  return {event: async ({event}) => {
    const props = event?.properties || {};
    if (!root && event?.type === 'session.created' && props.info?.id && !props.info.parentID) root = props.info.id;
    if (!root && event?.type === 'message.updated' && props.info?.role === 'user') root = props.info.sessionID || '';
    if (event?.type !== 'session.status' || !root || props.sessionID !== root || props.status?.type !== 'retry') return;
    process.stdout.write(JSON.stringify({type:'praimate.session.status',sessionID:root,status:props.status}) + '\n');
  }};
};
`

func openCodeStatusEnvironment(base map[string]string, sessionID string) (map[string]string, func(), error) {
	config := map[string]any{}
	raw, overridden := base["OPENCODE_CONFIG_CONTENT"]
	if !overridden {
		raw = os.Getenv("OPENCODE_CONFIG_CONTENT")
	}
	if raw != "" && (json.Unmarshal([]byte(raw), &config) != nil || config == nil) {
		return nil, nil, errors.New("invalid OpenCode launch configuration for status tracking")
	}
	plugins, _ := config["plugin"].([]any)
	if value, exists := config["plugin"]; exists && value != nil && plugins == nil {
		return nil, nil, errors.New("OpenCode launch plugins must be an array")
	}
	dir, err := os.MkdirTemp("", "praimate-status-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "status.mjs")
	if err := os.WriteFile(path, []byte(openCodeStatusPlugin), 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	config["plugin"] = append(plugins, (&url.URL{Scheme: "file", Path: p}).String())
	encoded, _ := json.Marshal(config)
	env := make(map[string]string, len(base)+2)
	for key, value := range base {
		env[key] = value
	}
	env["OPENCODE_CONFIG_CONTENT"] = string(encoded)
	env["PRAIMATE_STATUS_SESSION"] = sessionID
	return env, cleanup, nil
}

func openCodeRetryStatus(raw map[string]any) (string, error) {
	if payload, ok := raw["payload"].(map[string]any); ok {
		raw = payload
	}
	status, _ := raw["status"].(map[string]any)
	if props, ok := raw["properties"].(map[string]any); ok && status == nil {
		status, _ = props["status"].(map[string]any)
	}
	if stringFromMap(status, "type") != "retry" {
		return "", nil
	}
	message := stringFromMap(status, "message")
	if message == "" {
		message = "Provider temporarily unavailable"
	}
	action, _ := status["action"].(map[string]any)
	reason := stringFromMap(action, "reason")
	next := int64(0)
	if value, ok := status["next"].(float64); ok {
		next = int64(value)
	}
	detail := message
	if next > 0 {
		detail += "; next retry at " + time.UnixMilli(next).Format(time.RFC3339)
	}
	if attempt, ok := status["attempt"].(float64); ok {
		detail += " (attempt " + strconv.Itoa(int(attempt)) + ")"
	}
	lower := strings.ToLower(message)
	quota := reason == "free_tier_limit" || reason == "account_rate_limit" || strings.Contains(lower, "usage exceeded") || strings.Contains(lower, "usage limit") || strings.Contains(lower, "quota exceeded")
	if quota || next > time.Now().Add(30*time.Second).UnixMilli() {
		return detail, fmt.Errorf("%s; choose another model or retry later; partial activity and the CLI session were retained", detail)
	}
	return detail, nil
}
