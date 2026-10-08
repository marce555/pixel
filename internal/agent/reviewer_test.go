package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/scheduler"
)

type mockReviewLLMProvider struct {
	response string
}

func (m *mockReviewLLMProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return m.response, nil
}

func (m *mockReviewLLMProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- m.response
	close(out)
	close(errs)
	return out, errs
}

func (m *mockReviewLLMProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{}, nil
}

func TestReviewerAgentSuccess(t *testing.T) {
	mockResp := `---REVIEW_NOTES---
Structure validée. Un tableau comparatif complet a été ajouté.

---REVIEWED_TITLE---
Guide Avancé de Kubernetes et DevOps

---REVIEWED_KEYWORDS---
kubernetes cloud cluster

---REVIEWED_CONTENT---
<p>Introduction sur Kubernetes.</p>
<h2>Architecture</h2>
<p>Détail des pods et nodes.</p>
<table><thead><tr><th>Composant</th><th>Rôle</th></tr></thead><tbody><tr><td>Kubelet</td><td>Agent de nœud</td></tr></tbody></table>
<pre><code class="language-bash">kubectl get pods</code></pre>
`
	provider := &mockReviewLLMProvider{response: mockResp}
	reviewer := NewReviewerAgent(provider)

	draft := &scheduler.ArticleDraft{
		ID:         "test_draft_1",
		Title:      "Kubernetes & DevOps",
		Topic:      "Kubernetes",
		Category:   "Technologies",
		Keywords:   "kubernetes",
		RawContent: "<p>Texte brut initial...</p>",
		Status:     scheduler.DraftStatusDraft,
	}

	ctx := context.Background()
	title, keywords, content, notes, err := reviewer.Review(ctx, draft)
	if err != nil {
		t.Fatalf("Review failed: %v", err)
	}

	if title != "Guide Avancé de Kubernetes et DevOps" {
		t.Errorf("unexpected title: %s", title)
	}
	if keywords != "kubernetes cloud cluster" {
		t.Errorf("unexpected keywords: %s", keywords)
	}
	if !strings.Contains(content, "<table") {
		t.Errorf("expected content to contain table")
	}
	if !strings.Contains(content, "<pre><code") {
		t.Errorf("expected content to contain code block")
	}
	if !strings.Contains(notes, "Structure validée") {
		t.Errorf("unexpected notes: %s", notes)
	}
}

func TestReviewerAgentFallbackWhenNoProvider(t *testing.T) {
	reviewer := NewReviewerAgent(nil)

	draft := &scheduler.ArticleDraft{
		ID:         "test_draft_2",
		Title:      "Btrfs File System",
		Topic:      "Btrfs",
		Category:   "Technologies",
		Keywords:   "linux btrfs",
		RawContent: strings.Repeat("<p>Un paragraphe descriptif sur le système de fichiers Btrfs avec ses sous-volumes et ses snapshots pour garantir l'intégrité des données.</p>\n", 10),
		Status:     scheduler.DraftStatusDraft,
	}

	ctx := context.Background()
	title, keywords, content, notes, err := reviewer.Review(ctx, draft)
	if err != nil {
		t.Fatalf("Fallback review failed: %v", err)
	}

	if title != "Btrfs File System" {
		t.Errorf("unexpected title: %s", title)
	}
	if keywords != "linux btrfs" {
		t.Errorf("unexpected keywords: %s", keywords)
	}
	if !strings.Contains(content, "<table") {
		t.Errorf("expected fallback to insert table when missing")
	}
	if notes == "" {
		t.Errorf("expected non-empty notes")
	}
}

func TestParseReviewOutputWithPunctuation(t *testing.T) {
	input := `---REVIEW_NOTES---.
L'article a été audité avec succès. Les formules ont été corrigées.

---REVIEWED_TITLE---:
La Chasse aux Biosignatures Moléculaires

---REVIEWED_KEYWORDS---
exoplanet atmosphere, spectroscopy

---REVIEWED_CONTENT---
<p>L'exploration de l'Univers est fascinante.</p>
<h2>Détection</h2>
<p>Les données spectroscopiques confirment la présence de molécules.</p>
`
	title, keywords, content, notes := parseReviewOutput(input)

	if title != "La Chasse aux Biosignatures Moléculaires" {
		t.Errorf("unexpected title: %q", title)
	}
	if keywords != "exoplanet atmosphere, spectroscopy" {
		t.Errorf("unexpected keywords: %q", keywords)
	}
	if !strings.Contains(notes, "L'article a été audité avec succès") {
		t.Errorf("unexpected notes: %q", notes)
	}
	if strings.Contains(content, "REVIEW_NOTES") || strings.Contains(content, "REVIEWED_") {
		t.Errorf("delimiters leaked into content:\n%s", content)
	}
	if !strings.Contains(content, "L'exploration de l'Univers") {
		t.Errorf("expected content to contain intro paragraph")
	}
}

func TestParseReviewOutputWithContinuationLeak(t *testing.T) {
	// Simulates the exact bug where continuation appended a secondary review block into content
	input := `---REVIEW_NOTES---
Relecture initiale passée.

---REVIEWED_TITLE---
Exoplanètes et Biosignatures

---REVIEWED_KEYWORDS---
exoplanet astronomy

---REVIEWED_CONTENT---
<p>Première partie de l'article sur les biosignatures.</p>
<h2>Variations Saisonnières</h2>
<p>Les saisons influencent la chimie atmosphérique.</p>
---REVIEW_NOTES---.
L'article a été achevé en complétant la phrase tronquée sur les stratégies d'atténuation des variations saisonnières, en fermant toutes les balises HTML ouvertes.

La Chasse aux Biosignatures Moléculaires

exoplanet atmosphere, spectroscopy

<p>Deuxième partie de l'article avec la conclusion finale.</p>
`
	title, keywords, content, notes := parseReviewOutput(input)

	if title != "Exoplanètes et Biosignatures" {
		t.Errorf("unexpected title: %q", title)
	}
	if keywords != "exoplanet astronomy" {
		t.Errorf("unexpected keywords: %q", keywords)
	}
	if !strings.Contains(notes, "Relecture initiale passée") {
		t.Errorf("expected initial notes in notes: %q", notes)
	}
	if !strings.Contains(notes, "L'article a été achevé") {
		t.Errorf("expected secondary notes in notes: %q", notes)
	}
	// Content MUST NOT contain delimiter or notes
	if strings.Contains(content, "---REVIEW_NOTES---") || strings.Contains(content, "REVIEW_NOTES") {
		t.Errorf("REVIEW_NOTES delimiter leaked into content: %s", content)
	}
	if strings.Contains(content, "L'article a été achevé") {
		t.Errorf("reviewer notes explanation leaked into content: %s", content)
	}
	if strings.Contains(content, "exoplanet atmosphere, spectroscopy") {
		t.Errorf("leaked keywords should not be in content: %s", content)
	}
	if !strings.Contains(content, "Première partie") || !strings.Contains(content, "Deuxième partie") {
		t.Errorf("expected article content to be preserved: %s", content)
	}
}
