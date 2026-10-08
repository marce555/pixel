package llm

import (
	"context"
	"strings"
	"testing"
)

type mockDetailedProvider struct {
	responses []GenerationResult
	callIndex int
}

func (m *mockDetailedProvider) Generate(ctx context.Context, messages []Message) (string, error) {
	res, err := m.GenerateDetailed(ctx, messages, 4096)
	return res.Content, err
}

func (m *mockDetailedProvider) GenerateStream(ctx context.Context, messages []Message) (<-chan string, <-chan error) {
	out := make(chan string)
	close(out)
	return out, nil
}

func (m *mockDetailedProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func (m *mockDetailedProvider) GenerateDetailed(ctx context.Context, messages []Message, maxTokens int) (GenerationResult, error) {
	if m.callIndex < len(m.responses) {
		res := m.responses[m.callIndex]
		m.callIndex++
		return res, nil
	}
	return GenerationResult{Content: "default", FinishReason: "stop"}, nil
}

func TestGenerateWithContinuationStop(t *testing.T) {
	mock := &mockDetailedProvider{
		responses: []GenerationResult{
			{Content: "Article complet terminé.", FinishReason: "stop"},
		},
	}

	detected := false
	res, err := GenerateWithContinuation(context.Background(), mock, []Message{}, 4096, func(pass int) {
		detected = true
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detected {
		t.Errorf("expected no length detection on stop")
	}
	if res != "Article complet terminé." {
		t.Errorf("unexpected content: %q", res)
	}
	if mock.callIndex != 1 {
		t.Errorf("expected exactly 1 call, got %d", mock.callIndex)
	}
}

func TestGenerateWithContinuationLength(t *testing.T) {
	mock := &mockDetailedProvider{
		responses: []GenerationResult{
			{Content: "<article><p>Début du texte...", FinishReason: "length"},
			{Content: "Fin du texte et conclusion.</p></article>", FinishReason: "stop"},
		},
	}

	passCount := 0
	res, err := GenerateWithContinuation(context.Background(), mock, []Message{}, 4096, func(pass int) {
		passCount = pass
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if passCount != 1 {
		t.Errorf("expected passCount 1, got %d", passCount)
	}
	if !strings.Contains(res, "<article><p>Début du texte...") || !strings.Contains(res, "Fin du texte et conclusion.</p></article>") {
		t.Errorf("expected concatenated content, got %q", res)
	}
	if mock.callIndex != 2 {
		t.Errorf("expected exactly 2 calls, got %d", mock.callIndex)
	}
}
