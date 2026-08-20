package agent

import (
	"context"
	"os"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type mockPlaylistProvider struct {
	generateResp string
	lastMessages []llm.Message
}

func (m *mockPlaylistProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	m.lastMessages = messages
	return m.generateResp, nil
}

func (m *mockPlaylistProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- m.generateResp
	close(out)
	close(errs)
	return out, errs
}

func (m *mockPlaylistProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2}, nil
}

func TestResolveContextPlaylist(t *testing.T) {
	ctx := context.Background()
	coreMem := memory.NewCoreMemory("pixel_core_memory_playlist_test.json")
	defer os.Remove("pixel_core_memory_playlist_test.json")
	ltm := memory.NewLTM()

	provider := &mockPlaylistProvider{
		generateResp: "Soda Stereo || Los Prisioneros",
	}

	superior := NewSuperiorAgent(provider, coreMem, ltm, nil, nil, nil, nil, nil, nil)

	history := []llm.Message{
		{Role: llm.RoleUser, Content: "Propose moi des classiques du rock alternatif chilien"},
		{Role: llm.RoleAssistant, Content: "Je te propose d'écouter du Soda Stereo ou Los Prisioneros."},
	}

	resolved := superior.resolveContextPlaylist(ctx, history)
	if resolved != "Soda Stereo || Los Prisioneros" {
		t.Errorf("Expected 'Soda Stereo || Los Prisioneros', got %q", resolved)
	}

	// Verify that history was passed to the LLM call
	if len(provider.lastMessages) < 2 {
		t.Fatalf("Expected history messages to be passed to LLM, got only %d messages", len(provider.lastMessages))
	}

	// Test fallback when LLM returns none or empty
	provider.generateResp = "none"
	resolvedNone := superior.resolveContextPlaylist(ctx, history)
	if resolvedNone != "" {
		t.Errorf("Expected empty string for 'none' response, got %q", resolvedNone)
	}
}
