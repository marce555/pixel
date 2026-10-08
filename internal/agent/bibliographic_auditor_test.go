package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
)

type mockBiblioSearcher struct {
	results map[string]string
}

func (m *mockBiblioSearcher) SearchWeb(query string) (string, error) {
	if res, ok := m.results[query]; ok {
		return res, nil
	}
	return "James Thomson conducted human iPSC and embryonic stem cell research at the University of Wisconsin-Madison.", nil
}

func (m *mockBiblioSearcher) SearchWikipedia(query string) (string, error) {
	return "", nil
}

func (m *mockBiblioSearcher) ReadURLContent(targetURL string) (string, error) {
	return "", nil
}

func TestBibliographicAuditorAgentFixAffiliationAndChimera(t *testing.T) {
	mockJSON := `{
  "findings": [
    {
      "original_snippet": "le laboratoire de James Thomson à l'Université de Londres",
      "action": "CORRIGER",
      "replacement": "le laboratoire de James Thomson à l'Université du Wisconsin à Madison",
      "reason": "James Thomson a réalisé ses travaux pionniers à l'Université du Wisconsin à Madison, et non à Londres."
    },
    {
      "original_snippet": "<tr><td>Chimera, A. (2019)</td><td>Article</td><td>Étude fictive</td></tr>",
      "action": "SUPPRIMER / REMPLACER",
      "replacement": "",
      "reason": "Référence statistique inventée sans fondement réel."
    },
    {
      "original_snippet": "Yamanaka, S. (2007) Cell",
      "action": "CONSERVER",
      "replacement": "Yamanaka, S. (2007) Cell",
      "reason": "Article fondateur sur les iPSC humaines parfaitement authentique."
    }
  ]
}`

	provider := &mockReviewLLMProvider{response: mockJSON}
	searcher := &mockBiblioSearcher{results: map[string]string{}}
	auditor := NewBibliographicAuditorAgent(provider, searcher)
	auditor.SetCitationValidator(nil) // Isolate LLM reasoning in unit test without external network

	inputHTML := `<article>
<p>Des avancées remarquables ont été réalisées par le laboratoire de James Thomson à l'Université de Londres en reprogrammation cellulaire.</p>
<table>
<thead><tr><th>Référence</th><th>Type</th><th>Description</th></tr></thead>
<tbody>
<tr><td>Chimera, A. (2019)</td><td>Article</td><td>Étude fictive</td></tr>
<tr><td>Yamanaka, S. (2007) Cell</td><td>Article</td><td>Découverte des iPSC humaines</td></tr>
</tbody>
</table>
</article>`

	cleaned, report, err := auditor.Audit(context.Background(), "L'Architecte de la Mémoire Synthétique", inputHTML, nil)
	if err != nil {
		t.Fatalf("Audit failed: %v", err)
	}

	if report.Corrected != 1 {
		t.Errorf("Expected 1 correction, got %d", report.Corrected)
	}
	if report.Removed != 1 {
		t.Errorf("Expected 1 removed, got %d", report.Removed)
	}
	if report.Conserved != 1 {
		t.Errorf("Expected 1 conserved, got %d", report.Conserved)
	}

	if strings.Contains(cleaned, "Université de Londres") {
		t.Errorf("Expected 'Université de Londres' to be replaced, but it was found in result")
	}
	if !strings.Contains(cleaned, "Université du Wisconsin à Madison") {
		t.Errorf("Expected 'Université du Wisconsin à Madison' in result, but got:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "Chimera, A. (2019)") {
		t.Errorf("Expected 'Chimera, A. (2019)' to be removed, but it was found in result")
	}
	if !strings.Contains(cleaned, "Yamanaka, S. (2007) Cell") {
		t.Errorf("Expected valid reference to be conserved, but missing")
	}
}

type sequenceMockLLMProvider struct {
	step      int
	responses []string
}

func (s *sequenceMockLLMProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	if s.step < len(s.responses) {
		r := s.responses[s.step]
		s.step++
		return r, nil
	}
	return "", nil
}

func (s *sequenceMockLLMProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	resp, _ := s.Generate(ctx, messages)
	ch <- resp
	close(ch)
	close(errCh)
	return ch, errCh
}

func (s *sequenceMockLLMProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1}, nil
}

func TestBibliographicAuditorAgentInjectsSeminalReferencesWhenZeroRemain(t *testing.T) {
	// Step 0: Findings response: removes the single fake reference
	findingsJSON := `{
  "findings": [
    {
      "original_snippet": "<tr><td>Fake, A. (2020)</td><td>Article</td></tr>",
      "action": "SUPPRIMER / REMPLACER",
      "replacement": "",
      "reason": "Référence fictive sans fondement académique."
    }
  ]
}`

	// Step 1: Seminal candidates response: proposes real seminal paper on EGaIn
	seminalJSON := `{
  "publications": [
    {
      "authors": ["Dickey", "Whitesides"],
      "year": 2008,
      "title": "Eutectic Gallium-Indium (EGaIn): A Liquid Metal Alloy",
      "journal": "Advanced Functional Materials",
      "doi": "10.1002/adfm.200701216"
    }
  ]
}`

	provider := &sequenceMockLLMProvider{
		responses: []string{findingsJSON, seminalJSON},
	}

	searcher := &mockBiblioSearcher{results: map[string]string{}}
	auditor := NewBibliographicAuditorAgent(provider, searcher)
	auditor.SetCitationValidator(nil) // Isolated unit test without network

	inputHTML := `<article>
<h2>Actuateurs Soft Robotics</h2>
<p>Analyse des actuateurs à base de métaux liquides et réseaux vasculaires.</p>
<h3>Références et Sources Documentaires</h3>
<div class="table-responsive">
<table>
<thead><tr><th>Référence</th><th>Type</th></tr></thead>
<tbody>
<tr><td>Fake, A. (2020)</td><td>Article</td></tr>
</tbody>
</table>
</div>
</article>`

	cleaned, report, err := auditor.Audit(context.Background(), "L'Architecture des Actuateurs en Métal Liquide", inputHTML, nil)
	if err != nil {
		t.Fatalf("Audit failed: %v", err)
	}

	if report.Removed != 1 {
		t.Errorf("Expected 1 removed reference, got %d", report.Removed)
	}
	if report.ValidatedDOIs < 1 {
		t.Errorf("Expected at least 1 validated seminal DOI injected, got %d", report.ValidatedDOIs)
	}

	// Fake reference must be gone
	if strings.Contains(cleaned, "Fake, A. (2020)") {
		t.Errorf("Expected fake reference to be removed, but still present")
	}

	// Seminal reference must be injected with DOI
	if !strings.Contains(cleaned, "10.1002/adfm.200701216") {
		t.Errorf("Expected seminal DOI 10.1002/adfm.200701216 in table, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Dickey") || !strings.Contains(cleaned, "Whitesides") {
		t.Errorf("Expected authors Dickey, Whitesides in table, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Advanced Functional Materials") {
		t.Errorf("Expected journal in table, got:\n%s", cleaned)
	}
}

func TestBibliographicAuditorAgentInjectsSeminalWhenInstitutionalWasDeleted(t *testing.T) {
	// Simulated response: LLM deletes a generic institutional placeholder row (e.g. GIEC on non-climate topic)
	findingsJSON := `{
  "findings": [
    {
      "original_snippet": "<tr><td>Rapports GIEC</td><td>Rapports</td></tr>",
      "action": "SUPPRIMER / REMPLACER",
      "replacement": "",
      "reason": "Référence institutionnelle non pertinente pour ce sujet d'exoplanétologie."
    }
  ]
}`

	seminalJSON := `{
  "publications": [
    {
      "authors": ["Schwieterman", "Cockell"],
      "year": 2018,
      "title": "Exoplanet Biosignatures: A Review of Remotely Detectable Signs of Life",
      "journal": "Astrobiology",
      "doi": "10.1089/ast.2017.1729"
    }
  ]
}`

	provider := &sequenceMockLLMProvider{
		responses: []string{findingsJSON, seminalJSON},
	}

	searcher := &mockBiblioSearcher{results: map[string]string{}}
	auditor := NewBibliographicAuditorAgent(provider, searcher)
	auditor.SetCitationValidator(nil)

	inputHTML := `<article>
<h2>Atmosphères Exoplanétaires</h2>
<p>Recherche de biosignatures moléculaires.</p>
<h3>Références et Sources Documentaires</h3>
<div class="table-responsive">
<table>
<thead><tr><th>Référence</th><th>Type</th></tr></thead>
<tbody>
<tr><td>Rapports GIEC</td><td>Rapports</td></tr>
</tbody>
</table>
</div>
</article>`

	cleaned, report, err := auditor.Audit(context.Background(), "La Chasse aux Biosignatures Moléculaires", inputHTML, nil)
	if err != nil {
		t.Fatalf("Audit failed: %v", err)
	}

	// Institutional row must be deleted
	if strings.Contains(cleaned, "Rapports GIEC") {
		t.Errorf("Expected GIEC placeholder row to be deleted")
	}

	// Real seminal paper must be injected despite initial institutional presence
	if !strings.Contains(cleaned, "10.1089/ast.2017.1729") {
		t.Errorf("Expected seminal DOI 10.1089/ast.2017.1729 in table, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Schwieterman") {
		t.Errorf("Expected author Schwieterman in table, got:\n%s", cleaned)
	}
	if report.ValidatedDOIs < 1 {
		t.Errorf("Expected report.ValidatedDOIs >= 1, got %d", report.ValidatedDOIs)
	}
}


