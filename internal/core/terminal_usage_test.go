package core

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalUsageLocalAuthenticatedDeduplicated(t *testing.T) {
	c := nativeTestCore(t)
	u, err := c.BeginTerminalUsage(context.Background(), "copilot", "fallback", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	body := `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"trace","spanId":"span","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},{"key":"gen_ai.response.model","value":{"stringValue":"reported"}},{"key":"gen_ai.usage.input_tokens","value":{"intValue":"100"}},{"key":"gen_ai.usage.output_tokens","value":{"intValue":"20"}}]}]}]}]}`
	post := func(auth bool) int {
		r, _ := http.NewRequest("POST", u.endpoint+"/v1/traces", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.Header.Set("Authorization", "Bearer "+u.token)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if post(false) != http.StatusUnauthorized {
		t.Fatal("unauthenticated telemetry accepted")
	}
	for i := 0; i < 2; i++ {
		if post(true) != http.StatusOK {
			t.Fatal("valid telemetry rejected")
		}
	}
	d, err := c.UsageDashboard(context.Background(), "")
	if err != nil || d.Totals.Tokens != 120 || d.Totals.Runs != 1 || d.Models[0].Name != "reported" {
		t.Fatalf("dashboard=%+v err=%v", d, err)
	}
	if _, ok := u.Env["OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"]; !ok {
		t.Fatal("content capture not controlled")
	}
}

func TestTerminalUsageOpenCodeConfigAndCleanup(t *testing.T) {
	c := nativeTestCore(t)
	for _, raw := range []string{`null`, `{"plugin":"bad"}`, `[]`} {
		if u, err := c.BeginTerminalUsage(context.Background(), "opencode", "", map[string]string{"OPENCODE_CONFIG_CONTENT": raw}); err == nil {
			u.Close()
			t.Fatalf("accepted invalid configuration: %s", raw)
		}
	}
	u, err := c.BeginTerminalUsage(context.Background(), "opencode", "", map[string]string{"OPENCODE_CONFIG_CONTENT": `{"plugin":["existing"],"model":"provider/model"}`})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	var config map[string]any
	if err := json.Unmarshal([]byte(u.Env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	plugins := config["plugin"].([]any)
	if len(plugins) != 2 || plugins[0] != "existing" || config["model"] != "provider/model" {
		t.Fatal(config)
	}
	dir := u.pluginDir
	u.Close()
	u.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("plugin directory retained", err)
	}
	if res, err := http.Get(u.endpoint); err == nil {
		res.Body.Close()
		t.Fatal("receiver still listening")
	}
}

func TestOpenCodeUsagePluginPostsOnlyCompletedStepCounters(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "usage.mjs"), []byte(openCodeUsagePlugin), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import assert from 'node:assert/strict';
import {PrAImateUsage} from './usage.mjs';
const posts=[];
globalThis.fetch=async (url,opts)=>{posts.push({url,...opts});return {ok:true};};
process.env.PRAIMATE_USAGE_ENDPOINT='http://127.0.0.1:1/v1/usage';
process.env.PRAIMATE_USAGE_TOKEN='test';
const plugin=await PrAImateUsage();
await plugin.event({event:{type:'message.updated',properties:{info:{role:'assistant',id:'msg',modelID:'model',content:'private'}}}});
await plugin.event({event:{type:'message.part.updated',properties:{part:{type:'text',text:'private'}}}});
await plugin.event({event:{type:'message.part.updated',properties:{part:{id:'part',messageID:'msg',type:'step-finish',tokens:{input:10,output:5},text:'private'}}}});
assert.equal(posts.length,1);
assert.deepEqual(JSON.parse(posts[0].body),{id:'part',model:'model',tokens:{input:10,output:5}});
assert.equal(posts[0].headers.Authorization,'Bearer test');
globalThis.fetch=async()=>{throw Error('offline');};
await plugin.event({event:{type:'message.part.updated',properties:{part:{id:'p2',type:'step-finish',tokens:{input:1,output:1}}}}});
`
	path := filepath.Join(dir, "check.mjs")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, path).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestTerminalUsageParsesOnlyProviderReports(t *testing.T) {
	for _, tc := range []struct {
		cli, kind, body string
		input, output   int
	}{
		{"codex", "logs", `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","attributes":[{"key":"event.name","value":{"stringValue":"codex.sse_event"}},{"key":"event.kind","value":{"stringValue":"response.completed"}},{"key":"input_token_count","value":{"intValue":"100"}},{"key":"output_token_count","value":{"intValue":"20"}}]}]}]}]}`, 100, 20},
		{"claude", "logs", `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"1","attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},{"key":"input_tokens","value":{"intValue":"100"}},{"key":"output_tokens","value":{"intValue":"20"}},{"key":"cache_read_tokens","value":{"intValue":"50"}}]}]}]}]}`, 150, 20},
		{"opencode", "usage", `{"id":"step","model":"test","tokens":{"input":100,"output":20,"reasoning":10,"cache":{"read":50,"write":5}}}`, 155, 30},
	} {
		t.Run(tc.cli, func(t *testing.T) {
			events, err := parseTerminalUsage([]byte(tc.body), tc.cli, tc.kind)
			if err != nil || len(events) != 1 || events[0].Usage.PromptTokens != tc.input || events[0].Usage.CompletionTokens != tc.output {
				t.Fatalf("events=%+v err=%v", events, err)
			}
		})
	}
	// A tool output containing a fabricated usage-looking JSON string is not usage.
	body, _ := json.Marshal(map[string]any{"body": map[string]string{"stringValue": `{"input_token_count":900,"output_token_count":50}`}})
	events, err := parseTerminalUsage(body, "codex", "logs")
	if err != nil || len(events) != 0 {
		t.Fatal("interpreted text as token usage")
	}
}
