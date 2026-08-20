package memory

import (
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
)

// STM (Short Term Memory) represents the active, immediate context (RAM).
type STM struct {
	mu           sync.RWMutex
	messages     []llm.Message
	limit        int // Max messages to keep in STM
	lastActivity time.Time
}

func NewSTM(limit int) *STM {
	return &STM{
		messages:     make([]llm.Message, 0),
		limit:        limit,
		lastActivity: time.Now(),
	}
}

func (s *STM) AddMessage(msg llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, msg)
	s.lastActivity = time.Now()

	// If we exceed limit, we could trigger a "Sleep" or just drop oldest (FIFO)
	if len(s.messages) > s.limit {
		// Drop the oldest message (or ideally, summarize it later)
		s.messages = s.messages[1:]
	}
}

func (s *STM) GetMessages() []llm.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to avoid race conditions
	msgs := make([]llm.Message, len(s.messages))
	copy(msgs, s.messages)
	return msgs
}

func (s *STM) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = make([]llm.Message, 0)
}

func (s *STM) RemoveOldest(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if count > len(s.messages) {
		count = len(s.messages)
	}
	s.messages = s.messages[count:]
}

func (s *STM) IsFull() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages) >= s.limit
}

func (s *STM) GetLastActivity() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastActivity
}
