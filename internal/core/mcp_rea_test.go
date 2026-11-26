package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/installer"
)

func TestRegisterManagedREAUsesSharedCatalogueAndPreservesDisabledState(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required for the stdio fixture")
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	prefix, err := installer.ManagedToolPrefix(string(installer.ToolREA))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(prefix, "package")
	cli := filepath.Join(dir, "node_modules", "rea-agents", "scripts", "rea.mjs")
	if err := os.MkdirAll(filepath.Dir(cli), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, nil, 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(dir, "praimate-mcp.mjs")
	const fixture = `
import { createInterface } from 'node:readline';
for await (const line of createInterface({input: process.stdin})) {
  const {id, method} = JSON.parse(line);
  if (id == null) continue;
  const result = method === 'initialize'
    ? {protocolVersion:'2025-03-26', capabilities:{tools:{}}, serverInfo:{name:'rea-fixture',version:'1'}}
    : {tools:[{name:'inspect_artifact', inputSchema:{type:'object'}}]};
  console.log(JSON.stringify({jsonrpc:'2.0',id,result}));
}
`
	if err := os.WriteFile(launcher, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.RegisterManagedREA(ctx); err != nil {
		t.Fatal(err)
	}
	servers, err := c.ListMCPServers(ctx, true)
	if err != nil || len(servers) != 1 || servers[0].ID != managedREAMCPID || servers[0].Args[0] != launcher {
		t.Fatalf("enabled MCP catalogue: %+v, %v", servers, err)
	}
	probe, err := c.ProbeMCPServer(ctx, managedREAMCPID)
	if err != nil || !probe.OK || len(probe.Tools) != 1 || probe.Tools[0].Name != "inspect_artifact" {
		t.Fatalf("REA MCP probe: %+v, %v", probe, err)
	}
	if err := c.SetMCPEnabled(ctx, managedREAMCPID, false); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterManagedREA(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := c.GetMCPServer(ctx, managedREAMCPID)
	if err != nil || current.Enabled {
		t.Fatalf("repair changed disabled state: %+v, %v", current, err)
	}

	custom := ConnectMCPRequest{ID: managedREAMCPID, Name: "Custom fixture", Transport: MCPTransportHTTP, URL: "https://example.invalid/mcp"}
	if _, err := c.ConnectMCP(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if err := c.RegisterManagedREA(ctx); err == nil {
		t.Fatal("install replaced an unrelated custom MCP")
	}
	current, err = c.GetMCPServer(ctx, managedREAMCPID)
	if err != nil || current.URL != custom.URL {
		t.Fatalf("custom MCP changed: %+v, %v", current, err)
	}
}
