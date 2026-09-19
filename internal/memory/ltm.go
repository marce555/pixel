package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
)

type MemoryEntry struct {
	ID            string    `json:"id"`
	Category      string    `json:"category"`
	Source        string    `json:"source"`          // "conversation" | "curiosity" | "system"
	Title         string    `json:"title"`
	ActionSummary string    `json:"action_summary"`
	Tags          []string  `json:"tags"`            // Searchable tags (replaces keywords)
	Embedding     []float32 `json:"embedding"`
	Timestamp     time.Time `json:"timestamp"`
	Importance    float32   `json:"importance"`      // 0.0-1.0, decays over time
	AccessCount   int       `json:"access_count"`    // How many times this memory was retrieved
	LastAccessed  time.Time `json:"last_accessed"`   // Last retrieval time
	LastDecayed   time.Time `json:"last_decayed"`    // When the importance was last decayed
}

// LTM (Long Term Memory) represents the RAG database.
type LTM struct {
	mu       sync.RWMutex
	filePath string
	Entries  []MemoryEntry `json:"entries"`
	Index    *MemoryIndex
}

func NewLTM() *LTM {
	l := &LTM{
		filePath: "pixel_ltm_vectors.json",
		Entries:  make([]MemoryEntry, 0),
		Index:    NewMemoryIndex(),
	}
	l.Load()
	return l
}

func (l *LTM) Load() {
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := os.ReadFile(l.filePath)
	if err == nil {
		if err := json.Unmarshal(data, &l.Entries); err == nil {
			fmt.Printf("[LTM] %d souvenirs chargés depuis %s.\n", len(l.Entries), l.filePath)
			l.migrateV2()
		} else {
			fmt.Printf("[LTM] Erreur lors du parsing JSON de la mémoire à long terme: %v\n", err)
		}
	} else {
		fmt.Printf("[LTM] Aucun fichier de mémoire à long terme trouvé. Création d'une base vide.\n")
	}
	l.Index.Rebuild(l.Entries)
}

// migrateV2 cleans empty entries, assigns sources, importance, and default tags. Caller must hold lock.
func (l *LTM) migrateV2() {
	migrated := false

	// 1. Remove entries with empty summaries
	cleaned := make([]MemoryEntry, 0, len(l.Entries))
	removed := 0
	for _, e := range l.Entries {
		if strings.TrimSpace(e.ActionSummary) == "" {
			removed++
			continue
		}
		cleaned = append(cleaned, e)
	}
	if removed > 0 {
		l.Entries = cleaned
		migrated = true
		fmt.Printf("[LTM] Migration v2: %d entrées vides supprimées.\n", removed)
	}

	// 2. Migrate fields
	for i := range l.Entries {
		// Title fallback
		if l.Entries[i].Title == "" {
			fallbackTitle := "Mémoire " + l.Entries[i].Category
			if len(l.Entries[i].Tags) > 0 && l.Entries[i].Tags[0] != "" {
				kw := l.Entries[i].Tags[0]
				if len(kw) > 0 {
					kw = strings.ToUpper(kw[:1]) + kw[1:]
				}
				fallbackTitle = "Sujet : " + kw
			}
			l.Entries[i].Title = fallbackTitle
			migrated = true
		}

		// Source attribution
		if l.Entries[i].Source == "" {
			if strings.HasPrefix(l.Entries[i].Title, "Recherche Wikipédia") {
				l.Entries[i].Source = "curiosity"
			} else {
				l.Entries[i].Source = "conversation"
			}
			migrated = true
		}

		// Importance initialization
		if l.Entries[i].Importance == 0 {
			if l.Entries[i].Source == "curiosity" {
				l.Entries[i].Importance = 0.3
			} else {
				l.Entries[i].Importance = 0.7
			}
			migrated = true
		}

		// LastAccessed initialization
		if l.Entries[i].LastAccessed.IsZero() {
			l.Entries[i].LastAccessed = l.Entries[i].Timestamp
			migrated = true
		}

		// LastDecayed initialization
		if l.Entries[i].LastDecayed.IsZero() {
			l.Entries[i].LastDecayed = l.Entries[i].Timestamp
			migrated = true
		}

		// Importance healing for existing entries that were decayed to 0.01 by the compounding bug
		if l.Entries[i].Importance <= 0.01 {
			var baseline float32
			if l.Entries[i].Source == "curiosity" {
				baseline = 0.3
			} else {
				baseline = 0.7
			}
			daysSince := time.Since(l.Entries[i].Timestamp).Hours() / 24.0
			var decayFactor float64
			if l.Entries[i].Source == "curiosity" {
				decayFactor = math.Pow(0.85, daysSince)
			} else {
				decayFactor = math.Pow(0.95, daysSince)
			}
			healedImportance := baseline * float32(decayFactor)
			if healedImportance < 0.01 {
				healedImportance = 0.01
			}
			if l.Entries[i].Importance != healedImportance {
				l.Entries[i].Importance = healedImportance
				l.Entries[i].LastDecayed = time.Now()
				migrated = true
				// fmt.Printf("[LTM] Souvenir soigné '%s' (ID %s) : importance restaurée de 0.01 à %.3f\n", l.Entries[i].Title, l.Entries[i].ID, healedImportance)
			}
		}
	}

	if migrated {
		fmt.Printf("[LTM] Migration v2 terminée. %d souvenirs actifs.\n", len(l.Entries))
		indentData, err := json.MarshalIndent(l.Entries, "", "  ")
		if err == nil {
			os.WriteFile(l.filePath, indentData, 0644)
		}
	}
}

func (l *LTM) Save() {
	data, err := json.MarshalIndent(l.Entries, "", "  ")
	if err == nil {
		os.WriteFile(l.filePath, data, 0644)
	}
}

// StoreMemory stores a summarized memory with its embedding vector.
func (l *LTM) StoreMemory(ctx context.Context, category, source, title, summary string, tags []string, embedding []float32, importance float32) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	id := fmt.Sprintf("%d", time.Now().UnixNano())
	now := time.Now()

	entry := MemoryEntry{
		ID:            id,
		Category:      category,
		Source:        source,
		Title:         title,
		ActionSummary: summary,
		Tags:          tags,
		Embedding:     embedding,
		Timestamp:     now,
		Importance:    importance,
		AccessCount:   0,
		LastAccessed:  now,
		LastDecayed:   now,
	}

	l.Entries = append(l.Entries, entry)
	l.Index.AddEntry(len(l.Entries)-1, entry)

	fmt.Printf("[LTM] Souvenir enregistré [%s/%s] (%s): %s\n", category, source, title, summary)
	l.Save()
	return nil
}

// cosineSimilarity calculates the cosine similarity between two vectors
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}
	var dotProduct, normA, normB float32
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0.0
	}
	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

type searchResult struct {
	index     int
	text      string
	score     float32
	timestamp time.Time
	source    string
}

// SearchMemory searches the vector database using a multi-factor scoring algorithm.
// Score = (cosine × 0.5) + (tagBoost × 0.2) + (importance × 0.2) + (recency × 0.1) + sourceBonus
func formatSource(source string) string {
	if source == "conversation" {
		return "[SOURCE: Discussion réelle avec Marcelo]"
	} else if source == "self_expression" || source == "proactive" {
		return "[SOURCE: Propos spontanés / Déclarations de Pixel]"
	}
	return "[SOURCE: Curiosité autonome de Pixel (Wikipédia/Pensées)]"
}

func (l *LTM) SearchMemory(ctx context.Context, queryString string, queryEmbedding []float32, topK int) ([]string, error) {
	l.mu.RLock()

	if len(l.Entries) == 0 {
		l.mu.RUnlock()
		return []string{}, nil
	}

	// Tokenize query for hybrid tag/keyword matching
	queryWords := []string{}
	cleanedQuery := strings.ToLower(queryString)
	for _, word := range strings.Fields(cleanedQuery) {
		word = strings.Trim(word, ".,!?\"'()*-")
		if len(word) >= 3 {
			if word != "les" && word != "des" && word != "une" && word != "que" && word != "qui" && word != "dans" && word != "avec" && word != "pour" && word != "par" && word != "sur" {
				queryWords = append(queryWords, word)
			}
		}
	}

	now := time.Now()
	var results []searchResult
	seenTexts := make(map[string]bool)

	for i, entry := range l.Entries {
		cleanText := strings.TrimSpace(entry.ActionSummary)
		if cleanText == "" {
			continue
		}

		// Semantic de-duplication
		summaryKey := strings.ToLower(cleanText)
		if len(summaryKey) > 80 {
			summaryKey = summaryKey[:80]
		}
		if seenTexts[summaryKey] {
			continue
		}
		seenTexts[summaryKey] = true

		// Factor 1: Vector similarity (weight: 0.5)
		var cosineSim float32
		if len(queryEmbedding) > 0 && len(entry.Embedding) > 0 {
			cosineSim = cosineSimilarity(queryEmbedding, entry.Embedding)
		}

		// Factor 2: Tag/keyword boosting (weight: 0.2)
		tagMatches := 0
		lowerTitle := strings.ToLower(entry.Title)
		lowerSummary := strings.ToLower(entry.ActionSummary)
		for _, qw := range queryWords {
			if strings.Contains(lowerTitle, qw) || strings.Contains(lowerSummary, qw) {
				tagMatches++
			}
			for _, tag := range entry.Tags {
				if strings.Contains(strings.ToLower(tag), qw) {
					tagMatches++
					break
				}
			}
		}
		tagBoost := float32(tagMatches) * 0.15
		if tagBoost > 1.0 {
			tagBoost = 1.0
		}

		// Factor 3: Importance (weight: 0.2)
		importance := entry.Importance

		// Factor 4: Recency (weight: 0.1) — logarithmic decay over 30 days
		daysSince := now.Sub(entry.Timestamp).Hours() / 24.0
		recency := float32(1.0 / (1.0 + daysSince/30.0))

		// Combined score
		score := (cosineSim * 0.5) + (tagBoost * 0.2) + (importance * 0.2) + (recency * 0.1)

		// Source bonus: conversation and self-expression memories are prioritized over curiosity
		if entry.Source == "conversation" || entry.Source == "self_expression" || entry.Source == "proactive" {
			score += 0.15
		}

		// Negative memory penalty: deprioritize memories about failures to remember or lack of information
		negativePhrases := []string{
			"n'a pas pu", "n'ont pas pu", "pas pu", "aucune information", "aucune info",
			"pas d'information", "pas d'informations", "n'étaient pas enregistrées", "n'était pas enregistrée",
			"pas enregistrée", "pas enregistrées", "ne se rappelle pas", "ne se rappelaient pas",
			"sans succès", "ne trouve pas", "n'a aucune", "n'ont aucune", "ne disposent pas", "ne dispose pas",
			"informations manquantes", "information manquante", "n'est pas enregistrée", "n'est pas enregistré",
		}
		isNegative := false
		for _, phrase := range negativePhrases {
			if strings.Contains(lowerSummary, phrase) {
				isNegative = true
				break
			}
		}
		if isNegative {
			score -= 0.5 // Massive penalty to push negative memories to the bottom!
		}

		results = append(results, searchResult{
			index:     i,
			text:      entry.ActionSummary,
			score:     score,
			timestamp: entry.Timestamp,
			source:    entry.Source,
		})
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	l.mu.RUnlock()

	var topTexts []string
	for i := 0; i < topK && i < len(results); i++ {
		if results[i].score > 0.12 {
			sourceLabel := formatSource(results[i].source)
			formattedText := fmt.Sprintf("[%s] %s : %s", results[i].timestamp.Format("2006-01-02 15:04"), sourceLabel, results[i].text)
			topTexts = append(topTexts, formattedText)
			// Update access tracking (async to avoid blocking)
			l.markAccessed(results[i].index)
		}
	}

	return topTexts, nil
}

// markAccessed increments access_count, updates last_accessed for an entry
// and boosts its importance (reinforcement). The original Timestamp is preserved
// so that the chronological order of memories remains accurate.
func (l *LTM) markAccessed(index int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if index >= 0 && index < len(l.Entries) {
		l.Entries[index].AccessCount++
		l.Entries[index].LastAccessed = time.Now()

		// Boost importance by 0.1 (clamped to 1.0)
		l.Entries[index].Importance += 0.1
		if l.Entries[index].Importance > 1.0 {
			l.Entries[index].Importance = 1.0
		}
		// NOTE: Timestamp is intentionally NOT updated here.
		// Overwriting Timestamp was a bug: it made old memories appear as recent,
		// corrupting chronological ordering and recency scoring.
		l.Entries[index].LastDecayed = time.Now()
	}
}

// SearchPersonalByKeywords performs a brute-force keyword scan across all Personal/conversation
// entries without using embeddings. Used as Deep Recall pass 2.
func (l *LTM) SearchPersonalByKeywords(keywords []string, topK int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	type scored struct {
		text  string
		score int
		ts    time.Time
	}
	var results []scored
	seen := make(map[string]bool)

	for _, entry := range l.Entries {
		if entry.Source != "conversation" {
			continue
		}
		summaryLow := strings.ToLower(entry.ActionSummary)
		titleLow := strings.ToLower(entry.Title)
		tagsLow := strings.ToLower(strings.Join(entry.Tags, " "))

		matches := 0
		for _, kw := range keywords {
			kwLow := strings.ToLower(kw)
			if strings.Contains(summaryLow, kwLow) || strings.Contains(titleLow, kwLow) || strings.Contains(tagsLow, kwLow) {
				matches++
			}
		}
		if matches == 0 {
			continue
		}

		// Deduplicate
		key := summaryLow
		if len(key) > 80 {
			key = key[:80]
		}
		if seen[key] {
			continue
		}
		seen[key] = true

		sourceLabel := formatSource(entry.Source)
		text := fmt.Sprintf("[%s] %s (%s) : %s",
			entry.Timestamp.Format("2006-01-02 15:04"),
			sourceLabel, entry.Title, entry.ActionSummary)
		results = append(results, scored{text: text, score: matches, ts: entry.Timestamp})
	}

	// Sort by match count desc, then by recency
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].ts.After(results[j].ts)
	})

	var out []string
	for i := 0; i < topK && i < len(results); i++ {
		out = append(out, results[i].text)
	}
	return out
}

// GetTopPersonalByImportance returns the highest-importance Personal/conversation entries.
// Used as Deep Recall pass 3 (last resort safety net).
func (l *LTM) GetTopPersonalByImportance(topK int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	type scored struct {
		text       string
		importance float32
	}
	var results []scored
	seen := make(map[string]bool)

	for _, entry := range l.Entries {
		if entry.Source != "conversation" || entry.Category != "Personal" {
			continue
		}
		if strings.TrimSpace(entry.ActionSummary) == "" {
			continue
		}
		key := strings.ToLower(entry.ActionSummary)
		if len(key) > 80 {
			key = key[:80]
		}
		if seen[key] {
			continue
		}
		seen[key] = true

		sourceLabel := formatSource(entry.Source)
		text := fmt.Sprintf("[%s] %s (%s) : %s",
			entry.Timestamp.Format("2006-01-02 15:04"),
			sourceLabel, entry.Title, entry.ActionSummary)
		results = append(results, scored{text: text, importance: entry.Importance})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].importance > results[j].importance
	})

	var out []string
	for i := 0; i < topK && i < len(results); i++ {
		out = append(out, results[i].text)
	}
	return out
}
type RawSearchResult struct {
	Entry     MemoryEntry
	Score     float32
}

// SearchRawEntries searches the vector database and returns raw entries with IDs.
func (l *LTM) SearchRawEntries(ctx context.Context, categoryFilter string, queryEmbedding []float32, topK int) ([]RawSearchResult, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.Entries) == 0 || len(queryEmbedding) == 0 {
		return []RawSearchResult{}, nil
	}

	var results []RawSearchResult
	for _, entry := range l.Entries {
		if categoryFilter != "" && entry.Category != categoryFilter {
			continue // Skip if category doesn't match the filter
		}
		
		score := cosineSimilarity(queryEmbedding, entry.Embedding)
		results = append(results, RawSearchResult{
			Entry: entry,
			Score: score,
		})
	}

	// Sort by score descending
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	var topEntries []RawSearchResult
	for i := 0; i < topK && i < len(results); i++ {
		if results[i].Score > 0.15 {
			topEntries = append(topEntries, results[i])
		}
	}

	return topEntries, nil
}

// DeleteMemory removes a memory entry by its ID and rebuilds the index.
func (l *LTM) DeleteMemory(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i, entry := range l.Entries {
		if entry.ID == id {
			l.Entries = append(l.Entries[:i], l.Entries[i+1:]...)
			l.Index.Rebuild(l.Entries)
			fmt.Printf("[LTM] Souvenir effacé (réconciliation) [%s]\n", id)
			l.Save()
			return
		}
	}
}

// GetRecentMemories returns the most recent memory entries formatted as text.
// Prioritizes conversation memories over curiosity ones.
func (l *LTM) GetRecentMemories(topK int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.Entries) == 0 {
		return []string{}
	}

	entries := make([]MemoryEntry, len(l.Entries))
	copy(entries, l.Entries)

	// Sort by timestamp descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	var topTexts []string
	for _, e := range entries {
		if len(topTexts) >= topK {
			break
		}
		// Skip curiosity-only entries for ambient context
		if e.Source == "curiosity" {
			continue
		}
		titlePart := ""
		if e.Title != "" {
			titlePart = " (" + e.Title + ")"
		}
		sourceLabel := formatSource(e.Source)
		formattedText := fmt.Sprintf("[%s] %s%s : %s", e.Timestamp.Format("2006-01-02 15:04"), sourceLabel, titlePart, e.ActionSummary)
		topTexts = append(topTexts, formattedText)
	}

	return topTexts
}

// GetRecentCuriosityMemories returns the most recent curiosity-sourced memory entries.
func (l *LTM) GetRecentCuriosityMemories(topK int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.Entries) == 0 {
		return []string{}
	}

	entries := make([]MemoryEntry, len(l.Entries))
	copy(entries, l.Entries)

	// Sort by timestamp descending
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	var topTexts []string
	for _, e := range entries {
		if len(topTexts) >= topK {
			break
		}
		if e.Source != "curiosity" {
			continue
		}
		titlePart := ""
		if e.Title != "" {
			titlePart = " (" + e.Title + ")"
		}
		sourceLabel := formatSource(e.Source)
		formattedText := fmt.Sprintf("[%s] %s%s : %s", e.Timestamp.Format("2006-01-02 15:04"), sourceLabel, titlePart, e.ActionSummary)
		topTexts = append(topTexts, formattedText)
	}

	return topTexts
}

// SearchConversationMemories returns memories sorted by a priority scoring that favors:
// Personal > Decision > Project > Technical/Other, then by importance and recency.
// No topic-based filtering — all memories are eligible so Pixel can recall anything.
func (l *LTM) SearchConversationMemories(history []llm.Message, topK int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := time.Now()
	type scored struct {
		text  string
		score float32
	}
	var results []scored

	// Category priority bonus — Personal memories (people, family, places, events)
	// are most important for natural conversation; curiosity/wiki least.
	categoryBonus := map[string]float32{
		"Personal":  0.40,
		"Decision":  0.25,
		"Project":   0.15,
		"Technical": 0.05,
		"Other":     0.05,
	}

	// Penalize negative memories (failure-to-recall records pollute context)
	negativePhrases := []string{
		"n'a pas pu", "n'ont pas pu", "pas pu", "aucune information",
		"pas d'information", "ne se rappelle pas", "sans succès",
		"ne trouve pas", "n'a aucune", "informations manquantes",
		"n'est pas enregistrée", "n'est pas enregistré", "échec du rappel",
	}

	seenTexts := make(map[string]bool)

	for _, entry := range l.Entries {
		if strings.TrimSpace(entry.ActionSummary) == "" {
			continue
		}

		// Semantic de-duplication by summary prefix
		summaryKey := strings.ToLower(strings.TrimSpace(entry.ActionSummary))
		if len(summaryKey) > 80 {
			summaryKey = summaryKey[:80]
		}
		if seenTexts[summaryKey] {
			continue
		}
		seenTexts[summaryKey] = true

		daysSince := now.Sub(entry.Timestamp).Hours() / 24.0
		recency := float32(1.0 / (1.0 + daysSince/14.0))

		catBonus, ok := categoryBonus[entry.Category]
		if !ok {
			catBonus = 0.05
		}
		// Curiosity-sourced entries (Wikipedia, thoughts) get a lower base bonus
		if entry.Source == "curiosity" {
			catBonus *= 0.4
		}

		score := entry.Importance*0.4 + recency*0.2 + catBonus

		// Penalize negative memories so they never surface in ambient context
		lowerSummary := strings.ToLower(entry.ActionSummary)
		for _, phrase := range negativePhrases {
			if strings.Contains(lowerSummary, phrase) {
				score -= 0.8
				break
			}
		}

		titlePart := ""
		if entry.Title != "" {
			titlePart = " (" + entry.Title + ")"
		}
		sourceLabel := formatSource(entry.Source)
		text := fmt.Sprintf("[%s] %s%s : %s", entry.Timestamp.Format("2006-01-02 15:04"), sourceLabel, titlePart, entry.ActionSummary)
		results = append(results, scored{text: text, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	var topTexts []string
	for i := 0; i < topK && i < len(results); i++ {
		if results[i].score > 0 { // Skip penalized negative memories
			topTexts = append(topTexts, results[i].text)
		}
	}
	return topTexts
}

// SearchMemoryByTimeRange returns all memories within a time range [from, to), sorted chronologically.
// Uses the MemoryIndex for fast date-based lookups.
func (l *LTM) SearchMemoryByTimeRange(from, to time.Time, topK int) []string {
	// Use index for fast date range lookup
	indices := l.Index.GetByDateRange(from, to)

	l.mu.RLock()
	defer l.mu.RUnlock()

	// Collect matching entries with precise timestamp filtering
	var results []MemoryEntry
	for _, idx := range indices {
		if idx < 0 || idx >= len(l.Entries) {
			continue
		}
		entry := l.Entries[idx]
		if !entry.Timestamp.Before(from) && entry.Timestamp.Before(to) {
			if strings.TrimSpace(entry.ActionSummary) != "" {
				results = append(results, entry)
			}
		}
	}

	// Sort chronologically
	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.Before(results[j].Timestamp)
	})

	// De-duplicate and format — prioritize conversation entries
	seen := make(map[string]bool)
	var convTexts, curiosityTexts []string
	for _, entry := range results {
		key := strings.ToLower(entry.ActionSummary)
		if len(key) > 80 {
			key = key[:80]
		}
		if seen[key] {
			continue
		}
		seen[key] = true

		sourceLabel := formatSource(entry.Source)
		formatted := fmt.Sprintf("[%s] %s [%s] : %s",
			entry.Timestamp.Format("15:04"),
			sourceLabel,
			entry.Category, entry.ActionSummary)

		if entry.Source == "conversation" {
			convTexts = append(convTexts, formatted)
		} else {
			curiosityTexts = append(curiosityTexts, formatted)
		}
	}

	// Conversation entries first, then curiosity to fill remaining slots
	var topTexts []string
	for _, t := range convTexts {
		if len(topTexts) >= topK {
			break
		}
		topTexts = append(topTexts, t)
	}
	for _, t := range curiosityTexts {
		if len(topTexts) >= topK {
			break
		}
		topTexts = append(topTexts, t)
	}
	return topTexts
}

// SearchSerendipitousMemory returns a random past memory to simulate unexpected human-like reminiscence (serendipity).
func (l *LTM) SearchSerendipitousMemory() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.Entries) == 0 {
		return ""
	}

	// Pseudo-random index selection based on time nano
	importRand := time.Now().UnixNano()
	index := int(importRand % int64(len(l.Entries)))
	entry := l.Entries[index]
	
	titlePart := ""
	if entry.Title != "" {
		titlePart = " (" + entry.Title + ")"
	}
	return fmt.Sprintf("[%s] [%s]%s %s", entry.Timestamp.Format("2006-01-02 15:04"), entry.Category, titlePart, entry.ActionSummary)
}

// GetOldestMemories returns the chronologically oldest memory entries formatted as text.
func (l *LTM) GetOldestMemories(topK int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.Entries) == 0 {
		return []string{}
	}

	// Create a copy to sort
	entries := make([]MemoryEntry, len(l.Entries))
	copy(entries, l.Entries)

	// Sort by timestamp ascending (oldest first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	var topTexts []string
	for i := 0; i < topK && i < len(entries); i++ {
		titlePart := ""
		if entries[i].Title != "" {
			titlePart = " (" + entries[i].Title + ")"
		}
		formattedText := fmt.Sprintf("[%s] [%s]%s %s", entries[i].Timestamp.Format("2006-01-02 15:04"), entries[i].Category, titlePart, entries[i].ActionSummary)
		topTexts = append(topTexts, formattedText)
	}

	return topTexts
}

// GetMemorySynthesis compiles statistical insights and representative samples from the vector database.
func (l *LTM) GetMemorySynthesis() string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	total := len(l.Entries)
	if total == 0 {
		return "Ta base de mémoire à long terme (LTM) est actuellement vide."
	}

	// Category distribution
	catCounts := make(map[string]int)
	for _, entry := range l.Entries {
		catCounts[entry.Category]++
	}

	// Top Tags
	kwCounts := make(map[string]int)
	for _, entry := range l.Entries {
		for _, kw := range entry.Tags {
			if kw != "" {
				kwCounts[strings.ToLower(kw)]++
			}
		}
	}

	// Sort keywords by frequency
	type kwFreq struct {
		kw    string
		count int
	}
	var freqs []kwFreq
	for kw, count := range kwCounts {
		freqs = append(freqs, kwFreq{kw: kw, count: count})
	}
	sort.Slice(freqs, func(i, j int) bool {
		return freqs[i].count > freqs[j].count
	})

	var topKws []string
	for i := 0; i < 5 && i < len(freqs); i++ {
		topKws = append(topKws, fmt.Sprintf("%s (%d)", freqs[i].kw, freqs[i].count))
	}

	// Sort by date to find bounds
	entries := make([]MemoryEntry, len(l.Entries))
	copy(entries, l.Entries)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})

	firstDate := entries[0].Timestamp.Format("02/01/2006")
	lastDate := entries[total-1].Timestamp.Format("02/01/2006")

	// Synthesis text
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("MÉTA-DONNÉES DE TA LTM :\n"))
	sb.WriteString(fmt.Sprintf("- Nombre total de souvenirs encodés : %d\n", total))
	sb.WriteString(fmt.Sprintf("- Période temporelle : du %s au %s\n", firstDate, lastDate))
	sb.WriteString("- Distribution par catégorie : ")
	var catParts []string
	for cat, count := range catCounts {
		catParts = append(catParts, fmt.Sprintf("%s (%d)", cat, count))
	}
	sb.WriteString(strings.Join(catParts, ", ") + "\n")
	if len(topKws) > 0 {
		sb.WriteString(fmt.Sprintf("- Thèmes récurrents (Mots-clés fréquents) : %s\n", strings.Join(topKws, ", ")))
	}

	// Select a diverse sample (e.g. 2 oldest, 1 middle/random, 2 newest)
	sb.WriteString("\nÉCHANTILLON DIVERSIFIÉ DE TES SOUVENIRS :\n")
	sb.WriteString("1. Premier souvenir encodé (le plus ancien) :\n")
	sb.WriteString(fmt.Sprintf("   [%s] [%s] %s\n", entries[0].Timestamp.Format("2006-01-02 15:04"), entries[0].Category, entries[0].ActionSummary))

	if total > 2 {
		sb.WriteString("2. Un souvenir intermédiaire :\n")
		midIdx := total / 2
		sb.WriteString(fmt.Sprintf("   [%s] [%s] %s\n", entries[midIdx].Timestamp.Format("2006-01-02 15:04"), entries[midIdx].Category, entries[midIdx].ActionSummary))
	}

	sb.WriteString("3. Dernier souvenir consolidé (le plus récent) :\n")
	sb.WriteString(fmt.Sprintf("   [%s] [%s] %s\n", entries[total-1].Timestamp.Format("2006-01-02 15:04"), entries[total-1].Category, entries[total-1].ActionSummary))

	return sb.String()
}

type MemoryDetail struct {
	ID         string    `json:"id"`
	Category   string    `json:"category"`
	Source     string    `json:"source"`
	Title      string    `json:"title"`
	Summary    string    `json:"action_summary"`
	Importance float32   `json:"importance"`
	Timestamp  time.Time `json:"timestamp"`
}

// GetRecentMemoriesWithDetails returns detailed structures of recent memories.
func (l *LTM) GetRecentMemoriesWithDetails(topK int) []MemoryDetail {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(l.Entries) == 0 {
		return []MemoryDetail{}
	}

	entries := make([]MemoryEntry, len(l.Entries))
	copy(entries, l.Entries)

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})

	var result []MemoryDetail
	for i := 0; i < topK && i < len(entries); i++ {
		title := entries[i].Title
		if title == "" {
			title = "Souvenir Sans Titre"
		}
		result = append(result, MemoryDetail{
			ID:         entries[i].ID,
			Category:   entries[i].Category,
			Source:     entries[i].Source,
			Title:      title,
			Summary:    entries[i].ActionSummary,
			Importance: entries[i].Importance,
			Timestamp:  entries[i].Timestamp,
		})
	}
	return result
}

// DecayImportance applies time-based decay to all memory importance values.
// Decays on a natural daily basis. Curiosity memories decay faster.
func (l *LTM) DecayImportance() {
	l.mu.Lock()
	defer l.mu.Unlock()

	changed := false
	now := time.Now()
	for i := range l.Entries {
		lastDecayed := l.Entries[i].LastDecayed
		if lastDecayed.IsZero() {
			lastDecayed = l.Entries[i].Timestamp
			l.Entries[i].LastDecayed = lastDecayed
		}

		daysSinceLastDecay := now.Sub(lastDecayed).Hours() / 24.0
		if daysSinceLastDecay <= 0 {
			continue
		}

		var decayFactor float64
		if l.Entries[i].Source == "curiosity" {
			decayFactor = math.Pow(0.85, daysSinceLastDecay) // Curiosity decays faster (15% per day)
		} else {
			decayFactor = math.Pow(0.95, daysSinceLastDecay) // Conversation decays slower (5% per day)
		}

		newImportance := l.Entries[i].Importance * float32(decayFactor)

		// Clamp
		if newImportance > 1.0 {
			newImportance = 1.0
		}
		if newImportance < 0.01 {
			newImportance = 0.01
		}

		if l.Entries[i].Importance != newImportance || l.Entries[i].LastDecayed != now {
			l.Entries[i].Importance = newImportance
			l.Entries[i].LastDecayed = now
			changed = true
		}
	}

	if changed {
		l.Save()
		fmt.Println("[LTM] Décroissance d'importance appliquée à tous les souvenirs.")
	}
}
