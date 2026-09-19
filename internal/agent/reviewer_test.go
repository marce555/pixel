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
