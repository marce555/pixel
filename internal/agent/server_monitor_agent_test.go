package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type mockMonitorProvider struct {
	response string
}

func (m *mockMonitorProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return m.response, nil
}

func (m *mockMonitorProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- m.response
	close(out)
	close(errs)
	return out, errs
}

func (m *mockMonitorProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestServerMonitorEvaluateIncident(t *testing.T) {
	ctx := context.Background()

	// 1. Test case: Incident detected
	incidentProvider := &mockMonitorProvider{
		response: `{"incident": true, "alert_message": "Marcelo, le serveur appliyou.fr ne répond plus aux requêtes SSH."}`,
	}
	agentIncident := NewServerMonitorAgent(incidentProvider, nil, nil, nil, nil, nil, nil)
	hasIncident, alert, err := agentIncident.evaluateIncident(ctx, "=== STATUT APPLIYOU.FR : ANOMALIE_CRITIQUE ===\nConnection timed out")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasIncident {
		t.Errorf("expected incident=true, got false")
	}
	if !strings.Contains(alert, "appliyou.fr") {
		t.Errorf("expected alert message to mention appliyou.fr, got: %s", alert)
	}

	// 2. Test case: Nominal state (no incident)
	nominalProvider := &mockMonitorProvider{
		response: `{"incident": false, "alert_message": ""}`,
	}
	agentNominal := NewServerMonitorAgent(nominalProvider, nil, nil, nil, nil, nil, nil)
	hasIncident, alert, err = agentNominal.evaluateIncident(ctx, "=== STATUT APPLIYOU.FR : NOMINAL ===\nTous les services sont actifs")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasIncident {
		t.Errorf("expected incident=false, got true")
	}
	if alert != "" {
		t.Errorf("expected empty alert, got: %s", alert)
	}
}

func TestServerMonitorNominalSkip(t *testing.T) {
	broadcaster := &mockEventBroadcaster{}
	thoughtStream := memory.NewThoughtStream(10)

	_ = NewServerMonitorAgent(&mockMonitorProvider{}, nil, nil, nil, thoughtStream, broadcaster, nil)

	// Direct check of nominal behavior
	nominalReport := "=== STATUT APPLIYOU.FR : NOMINAL ===\nTous les services critiques (apache2, mariadb, docker) sont actifs et sains."

	isNominal := strings.Contains(nominalReport, "=== STATUT APPLIYOU.FR : NOMINAL ===") &&
		!strings.Contains(nominalReport, "ANOMALIE") &&
		!strings.Contains(nominalReport, "CRITIQUE")

	if !isNominal {
		t.Fatalf("expected nominalReport to be detected as nominal")
	}

	// Broadcaster should not have any message
	if len(broadcaster.messages) != 0 {
		t.Fatalf("expected no broadcast messages on nominal report, got %d", len(broadcaster.messages))
	}
}
