package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestAdaptiveProviderCreateEmbeddingSkips1MinAIAndQuotaCooldown(t *testing.T) {
	cloudCalled := false
	cloudServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cloudCalled = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer cloudServer.Close()

	localCalled := false
	localServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localCalled = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": []float32{0.5, 0.25}},
			},
		})
	}))
	defer localServer.Close()

	// Use 1min.ai cloud BaseURL marker via NewAdaptiveProvider, then override localProvider BaseURL to httptest server.
	p := NewAdaptiveProvider("cloud", "dummy-key", "https://api.1min.ai/openai/v1", "gpt-4.1-mini", localServer.URL, "qwen3.5:4b")
	p.cloudProvider.BaseURL = cloudServer.URL + "/1min.ai/v1"

	emb, err := p.CreateEmbedding(context.Background(), "bonjour")
	if err != nil {
		t.Fatalf("CreateEmbedding failed: %v", err)
	}
	if cloudCalled {
		t.Errorf("expected CreateEmbedding to skip 1min.ai cloud endpoint, but cloud server was called")
	}
	if !localCalled {
		t.Errorf("expected local embedding server to be called")
	}
	if len(emb) != 2 || emb[0] != 0.5 {
		t.Errorf("unexpected embedding result: %v", emb)
	}

	p.handleCloudError(errors.New("API error 429: Insufficient credits"))
	p.mu.RLock()
	remaining := time.Until(p.cloudCooldownUntil)
	p.mu.RUnlock()
	if remaining < 10*time.Minute {
		t.Errorf("expected >=10m cooldown for insufficient credits, got %v", remaining)
	}
}
