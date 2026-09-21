package studio

import "time"

// StudioState describes the installation and health state of PrAImate Studio.
type StudioState string

const (
	StateNotInstalled    StudioState = "not_installed"
	StateInstalling      StudioState = "installing"
	StateInstalled       StudioState = "installed"
	StateUpdateAvailable StudioState = "update_available"
	StateBroken          StudioState = "broken"
	StateRepairing       StudioState = "repairing"
	StateUnsupported     StudioState = "unsupported"
)

// StudioStatus describes the status of the PrAImate Studio installation and environment.
type StudioStatus struct {
	State           StudioState `json:"state"`
	Version         string      `json:"version"`
	CodeOSSVersion  string      `json:"codeOssVersion"`
	ProtocolVersion string      `json:"protocolVersion"`
	Platform        string      `json:"platform"`
	BinaryPath      string      `json:"binaryPath"`
	ExtensionPath   string      `json:"extensionPath"`
	SocketPath      string      `json:"socketPath"`
	BackendRunning  bool        `json:"backendRunning"`
	Error           string      `json:"error,omitempty"`
}

// LaunchOptions provides the configuration when opening a project in Studio.
type LaunchOptions struct {
	WorkspacePath string   `json:"workspacePath"`
	CLI           string   `json:"cli,omitempty"`
	Model         string   `json:"model,omitempty"`
	AgentID       string   `json:"agentId,omitempty"`
	Tools         string   `json:"tools,omitempty"`
	LocalEndpoint string   `json:"localEndpoint,omitempty"`
	LocalModel    string   `json:"localModel,omitempty"`
	MCPServers    []string `json:"mcpServers,omitempty"`
	Skills        []string `json:"skills,omitempty"`
}

// StudioSession represents an active or saved Studio session metadata.
type StudioSession struct {
	ID            string    `json:"id"`
	WorkspacePath string    `json:"workspacePath"`
	CreatedAt     time.Time `json:"createdAt"`
	CLI           string    `json:"cli"`
	Model         string    `json:"model"`
	AgentID       string    `json:"agentId"`
	Tools         string    `json:"tools"`
	MCPServers    []string  `json:"mcpServers"`
	Skills        []string  `json:"skills"`
}

// RPCRequest is a JSON-RPC 2.0 request payload.
type RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// RPCResponse is a JSON-RPC 2.0 response payload.
type RPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// RPCNotification is a JSON-RPC 2.0 server-to-client event.
type RPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// ClientCapabilities lists capabilities reported during handshake.
type ClientCapabilities struct {
	WorkspaceEdit bool `json:"workspaceEdit"`
	DiffView      bool `json:"diffView"`
	Terminal      bool `json:"terminal"`
	Approvals     bool `json:"approvals"`
}

// ServerCapabilities lists capabilities reported by the PrAImate Core RPC server.
type ServerCapabilities struct {
	Agents      bool `json:"agents"`
	Workflows   bool `json:"workflows"`
	Skills      bool `json:"skills"`
	MCP         bool `json:"mcp"`
	LocalModels bool `json:"localModels"`
	Approvals   bool `json:"approvals"`
	Streaming   bool `json:"streaming"`
	Runs        bool `json:"runs"`
}
