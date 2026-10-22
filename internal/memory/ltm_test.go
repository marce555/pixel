package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFoldString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Théologie", "theologie"},
		{"HANS URS VON BALTHASAR", "hans urs von balthasar"},
		{"Éthique théologique et esthétique", "ethique theologique et esthetique"},
		{"À côté de l'âme", "a cote de l'ame"},
		{"Ça va très bien", "ca va tres bien"},
	}

	for _, tt := range tests {
		got := FoldString(tt.input)
		if got != tt.expected {
			t.Errorf("FoldString(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestExtractKeywords(t *testing.T) {
	query := "Quels sont tes souvenirs concernant la theologie?"
	keywords := ExtractKeywords(query)

	if len(keywords) != 1 || keywords[0] != "theologie" {
		t.Fatalf("Expected keywords [theologie], got %v", keywords)
	}

	query2 := "Parle-moi de Hans Urs von Balthasar et de ses livres"
	keywords2 := ExtractKeywords(query2)
	expected := map[string]bool{"hans": true, "urs": true, "von": true, "balthasar": true, "livres": true}
	for _, kw := range keywords2 {
		if !expected[kw] {
			t.Errorf("Unexpected keyword: %s in %v", kw, keywords2)
		}
	}
}

func TestSearchMemoryTheologyRelevance(t *testing.T) {
	ltm := &LTM{
		filePath: "test_ltm_mock.json",
		Entries:  make([]MemoryEntry, 0),
		Index:    NewMemoryIndex(),
	}

	// 1. Old theology memories from months ago (importance decayed to 0.01, timestamp in June)
	pastTime := time.Now().Add(-90 * 24 * time.Hour)
	ltm.Entries = append(ltm.Entries, MemoryEntry{
		ID:            "theo_1",
		Category:      "Decision",
		Source:        "conversation",
		Title:         "Sujet : Théologie",
		ActionSummary: "L'utilisateur a proposé de changer le sujet du système d'ingénierie pour explorer la théologie esthétique de Hans Urs von Balthasar.",
		Tags:          []string{"théologie", "balthasar", "beauté divine"},
		Timestamp:     pastTime,
		Importance:    0.01,
		Embedding:     []float32{0.8, 0.6, 0.0, 0.0},
	})
	ltm.Entries = append(ltm.Entries, MemoryEntry{
		ID:            "theo_2",
		Category:      "Personal",
		Source:        "conversation",
		Title:         "Référence à Hans Urs von Balthasar",
		ActionSummary: "Échange sur la correction du nom de Hans Urs von Balthasar et sa pertinence pour la vision esthétique théologique de l'utilisateur.",
		Tags:          []string{"Hans Urs von Balthasar", "éthique théologique", "esthétique"},
		Timestamp:     pastTime,
		Importance:    0.01,
		Embedding:     []float32{0.75, 0.65, 0.0, 0.0},
	})

	// 2. Recent irrelevant memories from today (high recency, high importance, but completely unrelated topic)
	recentTime := time.Now().Add(-30 * time.Minute)
	ltm.Entries = append(ltm.Entries, MemoryEntry{
		ID:            "music_1",
		Category:      "Personal",
		Source:        "conversation",
		Title:         "Sujet : Musique",
		ActionSummary: "L'utilisateur a demandé une compilation de rock latino et Pixel a confirmé qu'il vient de lancer cette lecture.",
		Tags:          []string{"rock latino", "musique", "compilation"},
		Timestamp:     recentTime,
		Importance:    0.95,
		Embedding:     []float32{0.0, 0.0, 0.9, 0.4},
	})
	ltm.Entries = append(ltm.Entries, MemoryEntry{
		ID:            "robotics_1",
		Category:      "Technical",
		Source:        "self_expression",
		Title:         "Propos spontané : Soft Robotics",
		ActionSummary: "Pixel a partagé spontanément à Marcelo : Tiens, j'ai vu ton écran et je me disais que les robots qui marchent comme des humains fascinent.",
		Tags:          []string{"robotics", "actuators"},
		Timestamp:     recentTime,
		Importance:    0.9,
		Embedding:     []float32{0.0, 0.0, 0.85, 0.5},
	})

	// 3. Hallucinated negative memory that should be rejected
	ltm.Entries = append(ltm.Entries, MemoryEntry{
		ID:            "negative_1",
		Category:      "Other",
		Source:        "conversation",
		Title:         "Sujet : Theologie",
		ActionSummary: "Pixel a informé l'utilisateur qu'il n'a aucun souvenir de théologie ou d'auteurs religieux dans leur historique de conversation.",
		Tags:          []string{"theologie", "mémoire"},
		Timestamp:     recentTime,
		Importance:    0.7,
		Embedding:     []float32{0.7, 0.7, 0.0, 0.0},
	})

	ltm.Index.Rebuild(ltm.Entries)

	// Query: Vector aligned with theology {0.8, 0.6, 0.0, 0.0}
	queryVec := []float32{0.8, 0.6, 0.0, 0.0}
	results, err := ltm.SearchMemory(context.Background(), "Quels sont tes souvenirs concernant la theologie?", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchMemory error: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("Expected results, got 0")
	}

	// Verify that the first results are the genuine theology memories, NOT rock latino or robotics or negative memory
	foundTheo := false
	for _, res := range results {
		if strings.Contains(res, "rock latino") || strings.Contains(res, "robots qui marchent") {
			t.Errorf("Irrelevant memory appeared in results: %s", res)
		}
		if strings.Contains(res, "aucun souvenir") {
			t.Errorf("Negative memory appeared in results: %s", res)
		}
		if strings.Contains(res, "Balthasar") || strings.Contains(res, "théologie") {
			foundTheo = true
		}
	}

	if !foundTheo {
		t.Errorf("Expected to find Balthasar/théologie memory in results, got: %v", results)
	}
}

func TestSearchPersonalByKeywordsAccentFolding(t *testing.T) {
	ltm := &LTM{
		filePath: "test_ltm_mock.json",
		Entries:  make([]MemoryEntry, 0),
		Index:    NewMemoryIndex(),
	}

	ltm.Entries = append(ltm.Entries, MemoryEntry{
		ID:            "theo_1",
		Category:      "Personal",
		Source:        "conversation",
		Title:         "Sujet : Théologie",
		ActionSummary: "L'utilisateur explore la théologie esthétique de Hans Urs von Balthasar.",
		Tags:          []string{"théologie", "balthasar"},
		Timestamp:     time.Now().Add(-30 * 24 * time.Hour),
		Importance:    0.5,
	})

	// Search using unaccented "theologie"
	results := ltm.SearchPersonalByKeywords([]string{"theologie"}, 5)
	if len(results) == 0 {
		t.Fatalf("Expected to find entry with unaccented keyword 'theologie'")
	}

	if !strings.Contains(results[0], "Hans Urs von Balthasar") {
		t.Errorf("Expected Hans Urs von Balthasar in result, got: %s", results[0])
	}
}

func TestFormatTimestampWithRelative(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 23, 35, 0, time.Local)

	tests := []struct {
		name     string
		input    time.Time
		expected string
	}{
		{
			name:     "just now (< 1 min)",
			input:    now.Add(-20 * time.Second),
			expected: "Aujourd'hui à 08:23 (à l'instant)",
		},
		{
			name:     "2 minutes ago",
			input:    now.Add(-2 * time.Minute),
			expected: "Aujourd'hui à 08:21 (il y a 2 minutes)",
		},
		{
			name:     "1 minute ago",
			input:    now.Add(-1 * time.Minute),
			expected: "Aujourd'hui à 08:22 (il y a 1 minute)",
		},
		{
			name:     "2 hours ago (today)",
			input:    now.Add(-2 * time.Hour),
			expected: "Aujourd'hui à 06:23 (il y a 2 heures)",
		},
		{
			name:     "yesterday",
			input:    time.Date(2026, 10, 19, 14, 30, 0, 0, time.Local),
			expected: "Hier à 14:30",
		},
		{
			name:     "3 days ago",
			input:    time.Date(2026, 10, 17, 10, 15, 0, 0, time.Local),
			expected: "17/10 à 10:15 (il y a 3 jours)",
		},
		{
			name:     "2 weeks ago",
			input:    time.Date(2026, 10, 6, 9, 0, 0, 0, time.Local),
			expected: "2026-10-06 09:00 (il y a 2 semaines)",
		},
	}

	for _, tt := range tests {
		got := FormatTimestampWithRelativeAt(tt.input, now)
		if got != tt.expected {
			t.Errorf("[%s] got %q, want %q", tt.name, got, tt.expected)
		}
	}
}
