package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/scheduler"
)

type mockFactSearcher struct {
	webResults map[string]string
	wikiResult string
	urlContent map[string]string
}

func (m *mockFactSearcher) SearchWeb(query string) (string, error) {
	if m.webResults != nil {
		for k, v := range m.webResults {
			if strings.Contains(strings.ToLower(query), strings.ToLower(k)) {
				return v, nil
			}
		}
	}
	return "Résultats de recherche Web:\n- Titre: Documentation officielle\n  Description: Documentation de référence.\n  Lien: https://example.com/doc\n", nil
}

func (m *mockFactSearcher) SearchWikipedia(query string) (string, error) {
	if m.wikiResult != "" {
		return m.wikiResult, nil
	}
	return "Extrait Wikipédia sur le sujet technique.", nil
}

func (m *mockFactSearcher) ReadURLContent(targetURL string) (string, error) {
	if m.urlContent != nil {
		if content, ok := m.urlContent[targetURL]; ok {
			return content, nil
		}
	}
	return "Contenu officiel vérifié depuis la source web.", nil
}

type scriptedFactLLM struct {
	onGenerate func(messages []llm.Message) string
}

func (s *scriptedFactLLM) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return s.onGenerate(messages), nil
}

func (s *scriptedFactLLM) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- s.onGenerate(messages)
	close(out)
	close(errs)
	return out, errs
}

func (s *scriptedFactLLM) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{}, nil
}

func TestFactCheckerAdjustsInaccurateClaimAndRemovesFakeRef(t *testing.T) {
	searcher := &mockFactSearcher{
		webResults: map[string]string{
			"mir-29":   "Lien: https://ncbi.nlm.nih.gov/study\nDescription: miR-29 exhibits anti-fibrotic properties in hepatic tissue but does not regenerate tissue.",
			"fake-lab": "Lien: https://search.com\nDescription: Aucun résultat trouvé.",
		},
	}

	mockLLM := &scriptedFactLLM{
		onGenerate: func(messages []llm.Message) string {
			last := messages[len(messages)-1].Content
			switch {
			case strings.Contains(last, "AFFIRMATIONS FACTUELLES"):
				return `[
					{
						"claim": "Le microARN miR-29 régénère intégralement le foie en 48 heures",
						"excerpt": "Le microARN miR-29 régénère intégralement le foie en 48 heures selon les tests cliniques.",
						"kind": "scientifique",
						"query": "mir-29",
						"central": false
					}
				]`
			case strings.Contains(last, "AFFIRMATION À VÉRIFIER"):
				return `{
					"verdict": "imprécis",
					"evidence": "miR-29 a un rôle anti-fibrotique documenté mais ne régénère pas intégralement le foie en 48h.",
					"source_url": "https://ncbi.nlm.nih.gov/study",
					"correction": "Le microARN miR-29 exerce une action régulatrice anti-fibrotique documentée dans les tissus hépatiques."
				}`
			case strings.Contains(last, "tableau de sources"):
				return `{
					"verdict": "faux",
					"evidence": "Laboratoire et étude inexistants dans la littérature indexée.",
					"source_url": ""
				}`
			case strings.Contains(last, "Voici un bloc HTML extrait d'un article"):
				return `<p>Le microARN miR-29 exerce une action régulatrice anti-fibrotique documentée dans les tissus hépatiques selon les études publiées.</p>`
			default:
				return "{}"
			}
		},
	}

	fc := NewFactCheckerAgent(nil, searcher)
	fc.primaryOverride = mockLLM
	fc.labelOverride = "gemini-test-cloud"
	fc.concurrency = 1

	rawHTML := `<article class="blog-article">
<h2>Mécanismes Moléculaires</h2>
<p>Le microARN miR-29 régénère intégralement le foie en 48 heures selon les tests cliniques.</p>
<table>
<thead><tr><th>Référence</th><th>Description</th></tr></thead>
<tbody>
<tr id="row-fake"><td>FakeLab 2026</td><td>Étude fictive non vérifiable</td></tr>
</tbody>
</table>
</article>`

	var logs []string
	report, corrected, err := fc.Check(context.Background(), "Biologie Moléculaire", rawHTML, func(s string) {
		logs = append(logs, s)
	})

	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report == nil {
		t.Fatal("expected report to be non-nil")
	}

	if report.Verdict != scheduler.FactCheckCorrected {
		t.Errorf("expected verdict %s, got %s", scheduler.FactCheckCorrected, report.Verdict)
	}
	if !report.WasAdjusted() {
		t.Errorf("expected WasAdjusted() == true")
	}
	if report.Corrections != 1 {
		t.Errorf("expected 1 correction, got %d", report.Corrections)
	}
	if report.RemovedRefs != 1 {
		t.Errorf("expected 1 removed ref, got %d", report.RemovedRefs)
	}

	if strings.Contains(corrected, "FakeLab 2026") {
		t.Errorf("expected fake reference row to be removed from HTML")
	}
	if !strings.Contains(corrected, "anti-fibrotique documentée") {
		t.Errorf("expected corrected sentence in HTML, got:\n%s", corrected)
	}

	summary := scheduler.FormatFactCheckSummary(report)
	if !strings.Contains(summary, "a dû ajuster mes propos") {
		t.Errorf("expected summary to state that statements had to be adjusted, got:\n%s", summary)
	}
	if !strings.Contains(summary, "gemini-test-cloud") {
		t.Errorf("expected summary to mention independent model label, got:\n%s", summary)
	}
}

func TestFactCheckerValidatesAccurateArticleWithoutAdjustments(t *testing.T) {
	searcher := &mockFactSearcher{}
	mockLLM := &scriptedFactLLM{
		onGenerate: func(messages []llm.Message) string {
			last := messages[len(messages)-1].Content
			switch {
			case strings.Contains(last, "AFFIRMATIONS FACTUELLES"):
				return `[
					{
						"claim": "Le protocole BGP gère le routage inter-domaines sur Internet",
						"excerpt": "Le protocole BGP gère le routage inter-domaines sur Internet.",
						"kind": "technique",
						"query": "bgp routing",
						"central": true
					}
				]`
			case strings.Contains(last, "AFFIRMATION À VÉRIFIER"):
				return `{
					"verdict": "confirmé",
					"evidence": "RFC 4271 confirme le rôle de BGP comme protocole de routage inter-AS standard.",
					"source_url": "https://datatracker.ietf.org/doc/html/rfc4271",
					"correction": ""
				}`
			default:
				return "{}"
			}
		},
	}

	fc := NewFactCheckerAgent(nil, searcher)
	fc.primaryOverride = mockLLM
	fc.labelOverride = "gemini-test-cloud"
	fc.concurrency = 1

	rawHTML := `<article class="blog-article"><p>Le protocole BGP gère le routage inter-domaines sur Internet.</p></article>`
	report, corrected, err := fc.Check(context.Background(), "Réseau et BGP", rawHTML, nil)

	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if report.Verdict != scheduler.FactCheckValidated {
		t.Errorf("expected verdict %s, got %s", scheduler.FactCheckValidated, report.Verdict)
	}
	if report.WasAdjusted() {
		t.Errorf("expected WasAdjusted() == false")
	}
	if report.Confirmed != 1 {
		t.Errorf("expected 1 confirmed claim, got %d", report.Confirmed)
	}
	if corrected != rawHTML {
		t.Errorf("expected content to remain unchanged")
	}

	summary := scheduler.FormatFactCheckSummary(report)
	if !strings.Contains(summary, "aucun ajustement") {
		t.Errorf("expected summary to state that no adjustment was needed, got:\n%s", summary)
	}
}

func TestFactCheckerRejectsMisleadingArticle(t *testing.T) {
	searcher := &mockFactSearcher{}
	mockLLM := &scriptedFactLLM{
		onGenerate: func(messages []llm.Message) string {
			last := messages[len(messages)-1].Content
			switch {
			case strings.Contains(last, "AFFIRMATIONS FACTUELLES"):
				return `[
					{
						"claim": "Linux a été inventé en 1845",
						"excerpt": "Linux a été inventé en 1845.",
						"kind": "date",
						"query": "linux creation date",
						"central": true
					},
					{
						"claim": "Le noyau Linux fonctionne sans processeur",
						"excerpt": "Le noyau Linux fonctionne sans processeur.",
						"kind": "technique",
						"query": "linux kernel cpu",
						"central": false
					}
				]`
			case strings.Contains(last, "AFFIRMATION À VÉRIFIER"):
				return `{
					"verdict": "faux",
					"evidence": "Linux a été créé en 1991 par Linus Torvalds.",
					"source_url": "https://kernel.org",
					"correction": "Linux a été créé en 1991."
				}`
			case strings.Contains(last, "Voici un bloc HTML extrait d'un article"):
				// Simulate failure to correct central claim
				return "pas de html valide"
			default:
				return "{}"
			}
		},
	}

	fc := NewFactCheckerAgent(nil, searcher)
	fc.primaryOverride = mockLLM
	fc.labelOverride = "gemini-test-cloud"
	fc.concurrency = 1
	fc.rejectRatio = 0.4

	rawHTML := `<article><p>Linux a été inventé en 1845.</p><p>Le noyau Linux fonctionne sans processeur.</p></article>`
	report, _, err := fc.Check(context.Background(), "Histoire de Linux", rawHTML, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Verdict != scheduler.FactCheckRejected {
		t.Errorf("expected verdict %s, got %s", scheduler.FactCheckRejected, report.Verdict)
	}
}

func TestSanitizeDeepContent(t *testing.T) {
	input := `<article class="blog-article">
<p>L'eau devient dense par broutage de sel lors de la formation d'eau de mer givré (mergelé).</p>
<h3>Conséquences Géophysiques : Le Risque de Glissement du Pôle Nord</h3>
<p>Un ralentissement significatif de la circulation thermohaline pourrait déplacer le pôle Nord géographique vers l'est, dans la direction de l'Amérique du Nord.</p>
<p>De plus, la circulation thermohaline influence la tectonique des plaques. Bien que les effets sur la tectonique</p>
<div class="table-responsive">
<table>
<tr><td>Documentation Officielle &amp; Spécifications</td><td>Spécification technique / RFC / Standard</td></tr>
</table>
</div>
</article>`

	sanitized := SanitizeDeepContent(input, "L'Échafaudage Invisible : Dynamique Glaciaire")

	// 1. Vocabulary fixes
	if strings.Contains(sanitized, "broutage de sel") {
		t.Errorf("expected 'broutage de sel' to be replaced, got:\n%s", sanitized)
	}
	if !strings.Contains(sanitized, "rejet de saumure") {
		t.Errorf("expected 'rejet de saumure' to be present, got:\n%s", sanitized)
	}
	if strings.Contains(sanitized, "mergelé") {
		t.Errorf("expected 'mergelé' to be replaced by 'banquise' or 'glace de mer'")
	}

	// 2. Geophysical hallucination removed
	if strings.Contains(sanitized, "déplacer le pôle Nord géographique") {
		t.Errorf("expected North Pole displacement hallucination to be removed, got:\n%s", sanitized)
	}
	if strings.Contains(sanitized, "tectonique des plaques") {
		t.Errorf("expected plate tectonics hallucination to be removed, got:\n%s", sanitized)
	}

	// 3. Truncated sentence removed
	if strings.Contains(sanitized, "Bien que les effets sur la tectonique") {
		t.Errorf("expected incomplete sentence to be removed, got:\n%s", sanitized)
	}

	// 4. RFC table purged
	if strings.Contains(sanitized, "RFC / Standard") {
		t.Errorf("expected RFC table placeholder to be purged for climate article, got:\n%s", sanitized)
	}
}
