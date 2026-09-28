package core

import (
	"context"
	"errors"
)

// NativeWorkerReply is the transport result for a tool-free, stateless worker.
// Provider usage is absent when the endpoint does not report it.
type NativeWorkerReply struct {
	Content string
	Usage   *NativeUsage
}

// ExecuteNativeWorker reuses the native transport without creating a chat,
// inheriting agent permissions, or exposing repository tools to the model.
func ExecuteNativeWorker(ctx context.Context, route ChatLocalEndpoint, systemPrompt, task string) (*NativeWorkerReply, error) {
	return ExecuteNativeWorkerStream(ctx, route, systemPrompt, task, nil)
}

// ExecuteNativeWorkerStream optionally emits provider text deltas as they arrive.
func ExecuteNativeWorkerStream(ctx context.Context, route ChatLocalEndpoint, systemPrompt, task string, emit StreamHandler) (*NativeWorkerReply, error) {
	if route.Model == "" || route.Endpoint == "" || route.OutputTokens <= 0 {
		return nil, errors.New("native worker requires an endpoint, model and output token limit")
	}
	provider := nativeProvider{route: route}
	messages := []nativeMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: task}}
	reply, err := provider.turn(ctx, messages, nil, emit)
	if err != nil {
		return nil, err
	}
	if len(reply.ToolCalls) != 0 {
		return nil, errors.New("native worker returned an unrequested tool call")
	}
	return &NativeWorkerReply{Content: reply.Content, Usage: reply.Usage}, nil
}
