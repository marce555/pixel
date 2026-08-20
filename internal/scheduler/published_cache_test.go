package scheduler

import (
	"context"
	"testing"
)

func TestIsTopicTooSimilarLexicalOverlap(t *testing.T) {
	published := []string{
		"Neuroplasticité et Vieillissement : Révéler le Potentiel Cérébral des Personnes Âgées",
		"Sécurité Informatique et Machine Learning : La Convergence Déterminante de 2026",
		"La Décohérence Quantique : L'Obstacle Majeur à la Révolution de l'Informatique du Futur",
		"La Révolution DevOps avec l'Intelligence Artificielle",
		"btrfs raid5 write hole site:usenix.org",
	}

	tests := []struct {
		topic        string
		shouldMatch  bool
		expectedBase string
	}{
		{"Sécurité informatique et l'IA en 2026", true, "Sécurité Informatique et Machine Learning"},
		{"Révolution DevOps et Intelligence Artificielle dans les entreprises", true, "La Révolution DevOps avec l'Intelligence Artificielle"},
		{"btrfs raid5 write hole site:arxiv.org", true, "btrfs raid5 write hole site:usenix.org"},
		{"Recette de tarte aux pommes de grand-mère", false, ""},
	}

	ctx := context.Background()
	for _, tt := range tests {
		matched, tooSimilar := IsTopicTooSimilar(ctx, nil, tt.topic, published)
		if tooSimilar != tt.shouldMatch {
			t.Errorf("Pour le sujet '%s', attendu match=%v, obtenu match=%v (MatchedTitle='%s')", tt.topic, tt.shouldMatch, tooSimilar, matched)
		}
	}
}
