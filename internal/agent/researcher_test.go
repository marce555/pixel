package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
)

type MockAdaptiveProvider struct {
	llm.Provider
	CloudProvider llm.Provider
}

func (m *MockAdaptiveProvider) GetCloudProvider() llm.Provider {
	return m.CloudProvider
}

type MockLLM struct {
	Response string
}

func (m *MockLLM) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	// First check system prompt of the messages
	systemMsg := ""
	if len(messages) > 0 && messages[0].Role == llm.RoleSystem {
		systemMsg = messages[0].Content
	}

	if strings.Contains(systemMsg, "Pilote de l'Agent de Recherche") {
		// If history is longer than 2, it means we already completed one step, so finish.
		if len(messages) > 3 {
			return `{"type": "finish"}`, nil
		}
		return `{"type": "consult_gemini", "target": "Explain quantum decoherence"}`, nil
	}
	if strings.Contains(systemMsg, "IA cloud Gemini") {
		return "Quantum decoherence is the loss of quantum coherence.", nil
	}
	if strings.Contains(systemMsg, "assistant strict d'extraction de données") {
		return "Extracted: Quantum decoherence is the loss of quantum coherence.", nil
	}
	if strings.Contains(systemMsg, "Analyste de Données Expert") {
		return "Rapport final: decoherence.", nil
	}

	return m.Response, nil
}

func (m *MockLLM) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	return nil, nil
}

func (m *MockLLM) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1}, nil
}

func TestWebResearcherConsultGemini(t *testing.T) {
	mockBase := &MockLLM{Response: "test"}
	mockCloud := &MockLLM{Response: "Gemini Response"}
	
	adaptive := &MockAdaptiveProvider{
		Provider:      mockBase,
		CloudProvider: mockCloud,
	}

	researcher := NewWebResearcherAgent(adaptive, nil)
	report := researcher.RunResearch(context.Background(), "quantum decoherence")

	if !strings.Contains(report, "Rapport final") {
		t.Errorf("Expected final report, got: %s", report)
	}
}
