package core

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeRetryStopsQuotaBeforeEOFAndPreservesPartialReply(t *testing.T) {
	var events []StreamEvent
	raw := `{"type":"text","sessionID":"parent","part":{"text":"Partial answer"}}` + "\n" + `{"type":"praimate.session.status","sessionID":"child","status":{"type":"retry","message":"Child usage exceeded"}}` + "\n" + `{"type":"praimate.session.status","sessionID":"parent","status":{"type":"retry","message":"Free usage exceeded, subscribe to Go","action":{"reason":"free_tier_limit"}}}` + "\n"
	r := &noEOFReader{data: []byte(raw)}
	reply, err := parseOpenCodeStream(r, func(event StreamEvent) { events = append(events, event) })
	if err == nil || !strings.Contains(err.Error(), "choose another model") || reply.Text != "Partial answer" || reply.SessionID != "parent" {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
	if events[len(events)-1].Type != "error" {
		t.Fatal("quota not exposed as an error")
	}
}

type noEOFReader struct{ data []byte }

func (r *noEOFReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		panic("parser waited for EOF after a quota error")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestOpenCodeShortRetryContinuesAndLongRetryStops(t *testing.T) {
	for _, test := range []struct {
		wait time.Duration
		stop bool
	}{{time.Second, false}, {time.Hour, true}} {
		raw := fmt.Sprintf(`{"type":"session.status","sessionID":"parent","properties":{"status":{"type":"retry","message":"Provider overloaded","next":%d,"attempt":1}}}`+"\n", time.Now().Add(test.wait).UnixMilli())
		raw += `{"type":"text","sessionID":"parent","part":{"text":"Recovered"}}` + "\n"
		var events []StreamEvent
		reply, err := parseOpenCodeStream(strings.NewReader(raw), func(event StreamEvent) { events = append(events, event) })
		if (err != nil) != test.stop {
			t.Fatalf("wait=%s, err=%v", test.wait, err)
		}
		if !test.stop && (reply.Text != "Recovered" || events[0].Type != "retry") {
			t.Fatalf("short retry lost: %+v, %+v", reply, events)
		}
	}
}

func TestOpenCodeStatusPluginIsScopedAndLaunchOnly(t *testing.T) {
	base := map[string]string{"OPENCODE_CONFIG_CONTENT": `{"plugin":["existing"],"model":"provider/model"}`}
	env, cleanup, err := openCodeStatusEnvironment(base, "resume-id")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var config map[string]any
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatal(err)
	}
	if len(config["plugin"].([]any)) != 2 || config["model"] != "provider/model" || base["OPENCODE_CONFIG_CONTENT"] != `{"plugin":["existing"],"model":"provider/model"}` {
		t.Fatal("launch configuration changed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "status.mjs"), []byte(openCodeStatusPlugin), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import assert from 'node:assert/strict';
import {PrAImateStatus} from './status.mjs';
const lines=[]; process.stdout.write=(line)=>{lines.push(JSON.parse(line));return true;};
process.env.PRAIMATE_STATUS_SESSION='';
const p=await PrAImateStatus();
const emit=(type,properties)=>p.event({event:{type,properties}});
await emit('session.created',{info:{id:'child',parentID:'other'}});
await emit('session.created',{info:{id:'parent'}});
await emit('session.status',{sessionID:'child',status:{type:'retry',message:'child limit'}});
await emit('session.status',{sessionID:'parent',status:{type:'busy'}});
await emit('session.status',{sessionID:'parent',status:{type:'retry',message:'parent limit'}});
assert.equal(lines.length,1); assert.equal(lines[0].sessionID,'parent');
process.env.PRAIMATE_STATUS_SESSION='resumed'; const resumed=await PrAImateStatus();
await resumed.event({event:{type:'session.status',properties:{sessionID:'resumed',status:{type:'retry',message:'retry'}}}});
assert.equal(lines.length,2); assert.equal(lines[1].sessionID,'resumed');`
	cmd := exec.Command(node, "--input-type=module", "-e", script)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("status plugin: %v: %s", err, out)
	}
}
