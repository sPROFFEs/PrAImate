package core

import (
	"context"
	"fmt"
	"testing"
)

func TestAssistantReadsNewestChatMessagesInOrder(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{Title: "Assistant retrieval", CLIAgent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if _, err := c.AddMessage(ctx, chat.ID, "user", fmt.Sprintf("message %d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := c.RecentChatMessages(ctx, chat.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Content != "message 9" || messages[2].Content != "message 11" {
		t.Fatalf("unexpected recent messages: %+v", messages)
	}
}
