package studio

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// AssistantHooks shares the desktop operator and its encrypted session. Studio
// never launches a second model or writes a second copy of assistant settings.
type AssistantHooks struct {
	Call func(context.Context, string, json.RawMessage) (any, error)
}

func SetDesktopAssistantHooks(c *core.Core, hooks *AssistantHooks) {
	desktopServers.Lock()
	defer desktopServers.Unlock()
	desktopServers.assistant = hooks
	desktopServers.assistantCore = c
	if desktopServers.server != nil && desktopServers.server.core == c {
		desktopServers.server.mu.Lock()
		desktopServers.server.assistant = hooks
		desktopServers.server.mu.Unlock()
	}
}

func (s *Server) assistantCall(ctx context.Context, method string, body json.RawMessage) (any, error) {
	owner := s
	if s.workerOwner != nil {
		owner = s.workerOwner
	}
	owner.mu.Lock()
	hooks := owner.assistant
	owner.mu.Unlock()
	if method == "assistant.available" {
		return hooks != nil, nil
	}
	if hooks == nil || hooks.Call == nil {
		return nil, errors.New("Assistant & Voice require the PrAImate desktop service. Open Studio from PrAImate and reconnect; the desktop window may stay hidden")
	}
	result, err := hooks.Call(ctx, method, body)
	if method == "voice.begin" && err == nil {
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, marshalErr
		}
		var lease struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &lease); err != nil {
			return nil, err
		}
		s.mu.Lock()
		cancelled := ctx.Err() != nil
		if !cancelled {
			s.voiceLease = lease.ID
		}
		s.mu.Unlock()
		if cancelled {
			end, _ := json.Marshal(lease)
			_, _ = hooks.Call(context.Background(), "voice.end", end)
			return nil, ctx.Err()
		}
	}
	return result, err
}

// PublishDesktopAssistant sends only to initialized connections for this Core.
func PublishDesktopAssistant(c *core.Core, method string, value any) {
	desktopServers.Lock()
	s := desktopServers.server
	if s != nil && s.core != c {
		s = nil
	}
	desktopServers.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	clients := make([]*Server, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.Unlock()
	for _, client := range clients {
		client.mu.Lock()
		authenticated := client.authenticated
		client.mu.Unlock()
		if authenticated {
			client.Broadcast(method, value)
		}
	}
}
