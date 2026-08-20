package memory

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Thought struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Embedding []float32 `json:"embedding"`
	CreatedAt time.Time `json:"created_at"`
}

type ThoughtStream struct {
	mu       sync.RWMutex
	thoughts []Thought
	maxSize  int
}

func NewThoughtStream(maxSize int) *ThoughtStream {
	return &ThoughtStream{
		thoughts: make([]Thought, 0),
		maxSize:  maxSize,
	}
}

func (ts *ThoughtStream) AddThought(content string, embedding []float32) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	thought := Thought{
		ID:        uuid.New().String(),
		Content:   content,
		Embedding: embedding,
		CreatedAt: time.Now(),
	}

	ts.thoughts = append(ts.thoughts, thought)
	if len(ts.thoughts) > ts.maxSize {
		ts.thoughts = ts.thoughts[1:] // Keep it bounded
	}
}

// FindResonantThoughts returns thoughts that are semantically close to the target vector.
func (ts *ThoughtStream) FindResonantThoughts(ctx context.Context, target []float32, limit int, threshold float32) []Thought {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	if len(target) == 0 {
		return nil
	}

	type scoreStruct struct {
		score   float32
		thought Thought
	}
	var scores []scoreStruct

	for _, t := range ts.thoughts {
		if len(t.Embedding) != len(target) {
			continue
		}
		sim := cosineSimilarity(t.Embedding, target)
		if sim >= threshold {
			scores = append(scores, scoreStruct{score: sim, thought: t})
		}
	}

	// Simple bubble sort for finding top K
	for i := 0; i < len(scores); i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[j].score > scores[i].score {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}

	var results []Thought
	for i := 0; i < len(scores) && i < limit; i++ {
		results = append(results, scores[i].thought)
	}

	return results
}

// GetRecentThoughts returns the most recent thoughts in chronological order (or reverse).
func (ts *ThoughtStream) GetRecentThoughts(limit int) []Thought {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	if len(ts.thoughts) == 0 {
		return nil
	}

	var results []Thought
	start := len(ts.thoughts) - limit
	if start < 0 {
		start = 0
	}
	for i := len(ts.thoughts) - 1; i >= start; i-- {
		results = append(results, ts.thoughts[i])
	}
	return results
}


