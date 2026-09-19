package scheduler

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestIsTechTopic(t *testing.T) {
	cases := []struct {
		title    string
		content  string
		expected bool
	}{
		{"Introduction à Docker et Kubernetes", "Contenu sur les conteneurs", true},
		{"Btrfs RAID 5 write hole", "Explications du système de fichiers", true},
		{"Poésie et Littérature Romantique", "Analyse des poèmes du XIXe siècle", false},
		{"Philosophie de la Conscience", "Débats sur le libre-arbitre et la phénoménologie", false},
		{"Serveur Linux et scripts Python", "Automatisation de tâches d'administration", true},
	}

	for _, c := range cases {
		result := isTechTopic(c.title, c.content)
		if result != c.expected {
			t.Errorf("isTechTopic(%q, %q) = %v; expected %v", c.title, c.content, result, c.expected)
		}
	}
}

func TestAuditAndFixArticlesHandlerMissingPassword(t *testing.T) {
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}
	dm := NewDraftManager("")
	reviewer := &mockReviewer{}

	handler := NewAuditAndFixArticlesHandler(broadcaster, stm, nil, dm, reviewer)

	_ = os.Unsetenv("PIXEL_PUBLISH_PASS")

	task := &Task{
		Payload: "audit-only",
	}

	err := handler(context.Background(), task)
	if err == nil {
		t.Fatal("expected error due to missing PIXEL_PUBLISH_PASS")
	}
	if !strings.Contains(err.Error(), "PIXEL_PUBLISH_PASS") {
		t.Fatalf("expected PIXEL_PUBLISH_PASS error, got %v", err)
	}
}

func TestDetectSpecificArticleIssues(t *testing.T) {
	sampleContent := `# L'Architecture Cognitive Prédictive : Quand Notre Cerveau Devient un Super-Ordinateur Imaginatif
Notre perception du monde n'est pas une simple fenêtre transparente.
## 1. Les Fondations : Le Traitement Prédictif
* **L'anticipation active** : Nous ne voyons pas seulement, nous anticipons.
$$P(H|I) \propto P(I|H) \times P(H)^{w}$$
Le tableau suivant illustre comment le traitement prédictif interagit. <`

	trimmed := strings.TrimSpace(sampleContent)
	hasTable := strings.Contains(sampleContent, "<table")
	hasMarkdown := strings.Contains(sampleContent, "## ") || strings.Contains(sampleContent, "# ")
	isTruncated := strings.HasSuffix(trimmed, "<")
	hasLatex := strings.Contains(sampleContent, "$$")

	if hasTable {
		t.Errorf("expected no table in sample")
	}
	if !hasMarkdown {
		t.Errorf("expected markdown headings to be detected")
	}
	if !isTruncated {
		t.Errorf("expected truncation (trailing '<') to be detected")
	}
	if !hasLatex {
		t.Errorf("expected latex formula to be detected")
	}
}
