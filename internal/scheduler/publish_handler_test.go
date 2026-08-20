package scheduler

import (
	"context"
	"testing"

	"github.com/marce555/pixel/internal/llm"
)

type mockEventBroadcaster struct {
	messages []string
}

func (m *mockEventBroadcaster) Broadcast(message string) {
	m.messages = append(m.messages, message)
}

type mockSTMWriter struct {
	messages []llm.Message
}

func (m *mockSTMWriter) AddMessage(msg llm.Message) {
	m.messages = append(m.messages, msg)
}

func TestPublishArticleHandlerInvalidPayload(t *testing.T) {
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}
	handler := NewPublishArticleHandler(broadcaster, stm, nil, nil)

	ctx := context.Background()
	task := &Task{
		Payload: "invalid json",
	}

	err := handler(ctx, task)
	if err == nil {
		t.Fatal("expected handler to fail with invalid JSON payload")
	}
}
