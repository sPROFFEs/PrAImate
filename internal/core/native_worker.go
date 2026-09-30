package core

import (
	"context"
	"errors"
	"fmt"
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
	messages := []nativeMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: task}}
	if route.ContextTokens == 0 {
		route.ContextTokens = autoContextWindow(route.Model, 0)
	}
	status := nativeContextBudget(route, NativeContextStatus{})
	input := nativeMessageTokens(messages)
	if !validNativeWindow(route.ContextTokens) || status.InputLimit < 256 || input > status.InputLimit {
		return nil, fmt.Errorf("worker context budget exceeded: ~%d input tokens, %d available (%d window, %d output reserve); narrow the delegated task or adjust the host limits", input, status.InputLimit, route.ContextTokens, route.OutputTokens)
	}
	status.EstimatedInput = input
	route.OutputTokens = nativeOutputLimit(route, status)
	provider := nativeProvider{route: route}
	reply, err := provider.turn(ctx, messages, nil, emit)
	if err != nil {
		return nil, err
	}
	if len(reply.ToolCalls) != 0 {
		return nil, errors.New("native worker returned an unrequested tool call")
	}
	return &NativeWorkerReply{Content: reply.Content, Usage: reply.Usage}, nil
}
