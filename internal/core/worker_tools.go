package core

import (
	"context"
	"encoding/json"
)

// WorkerToolBroker exposes the existing managed project and command tools to
// orchestrated workers. Permissions and approvals remain owned by Core.
type WorkerToolBroker struct{ broker *managedToolBroker }

func NewWorkerToolBroker(ctx context.Context, root string, allowEdits, allowCommands bool, approval *ApprovalConfig) (*WorkerToolBroker, error) {
	capabilities := AgentCapabilities{ReadProject: true, UseGit: allowCommands, ModifyFiles: allowEdits, ExecuteCommands: allowCommands}
	broker, err := newManagedToolBroker(ctx, nil, capabilities, root, approval, nil)
	if err != nil {
		return nil, err
	}
	return &WorkerToolBroker{broker: broker}, nil
}

func (b *WorkerToolBroker) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	return b.broker.ExecuteTool(ctx, name, arguments)
}

func (b *WorkerToolBroker) Close() error { return b.broker.Close() }
