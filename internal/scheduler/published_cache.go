package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/marce555/pixel/internal/llm"
)

const PublishedArticlesFile = "published_articles.json"

// LoadPublishedArticles reads the cached published article titles from the local JSON file.
func LoadPublishedArticles() ([]string, error) {
	data, err := os.ReadFile(PublishedArticlesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var titles []string
	if err := json.Unmarshal(data, &titles); err != nil {
		return []string{}, err
	}
	if titles == nil {
		titles = []string{}
	}
	return titles, nil
}

// SavePublishedArticles writes the published article titles to the local JSON file.
func SavePublishedArticles(titles []string) error {
	if titles == nil {
		titles = []string{}
	}
	data, err := json.MarshalIndent(titles, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(PublishedArticlesFile, data, 0644)
}

// AddPublishedArticle adds a single title to the local cache if it doesn't already exist.
func AddPublishedArticle(title string) error {
	titles, err := LoadPublishedArticles()
	if err != nil {
		return err
	}

	titleLower := strings.ToLower(strings.TrimSpace(title))
	for _, t := range titles {
		if strings.ToLower(strings.TrimSpace(t)) == titleLower {
			return nil
		}
	}

	titles = append(titles, title)
	return SavePublishedArticles(titles)
}

// SyncPublishedArticles fetches the current list of published articles from AppliYou and updates the local cache.
func SyncPublishedArticles(ctx context.Context) ([]string, error) {
	titles, err := FetchPublishedArticles(ctx)
	if err != nil {
		return nil, err
	}
	if err := SavePublishedArticles(titles); err != nil {
		return nil, err
	}
	return titles, nil
}

var frenchStopWords = map[string]bool{
	"la": true, "le": true, "les": true, "l": true, "de": true, "des": true, "du": true,
	"d": true, "un": true, "une": true, "en": true, "et": true, "à": true, "a": true,
	"dans": true, "sur": true, "pour": true, "par": true, "avec": true, "ce": true,
	"cet": true, "cette": true, "ces": true, "au": true, "aux": true, "est": true,
	"sont": true, "qui": true, "que": true, "quoi": true, "comment": true, "plus": true,
	"ne": true, "pas": true, "se": true, "sa": true, "son": true, "ses": true, "mon": true,
	"ma": true, "mes": true, "ton": true, "ta": true, "tes": true, "notre": true, "nos": true,
	"votre": true, "vos": true, "leur": true, "leurs": true, "ou": true, "où": true,
}

func cleanSearchOperators(text string) string {
	clean := text
	for _, op := range []string{"site:", "filetype:", "ext:"} {
		for {
			idx := strings.Index(strings.ToLower(clean), op)
			if idx == -1 {
				break
			}
			end := idx + len(op)
			for end < len(clean) && clean[end] != ' ' {
				end++
			}
			clean = clean[:idx] + " " + clean[end:]
		}
	}
	return strings.TrimSpace(clean)
}

func extractKeywords(text string) map[string]bool {
	clean := cleanSearchOperators(text)
	clean = strings.ToLower(clean)
	replacer := strings.NewReplacer("-", " ", ":", " ", "'", " ", "’", " ", ",", " ", ".", " ", "?", " ", "!", " ", "\"", " ", "(", " ", ")", " ", "/", " ")
	clean = replacer.Replace(clean)
	words := strings.Fields(clean)

	keywords := make(map[string]bool)
	for _, w := range words {
		w = strings.TrimSpace(w)
		if len(w) > 2 && !frenchStopWords[w] {
			keywords[w] = true
		}
	}
	return keywords
}

func calculateOverlap(set1, set2 map[string]bool) (int, float64) {
	if len(set1) == 0 || len(set2) == 0 {
		return 0, 0
	}
	intersection := 0
	for k := range set1 {
		if set2[k] {
			intersection++
		}
	}
	smaller := len(set1)
	if len(set2) < smaller {
		smaller = len(set2)
	}
	return intersection, float64(intersection) / float64(smaller)
}

// IsTopicTooSimilar checks if the proposed topic is too similar to any of the already published article titles.
// Returns the title of the similar article if found, and a boolean.
func IsTopicTooSimilar(ctx context.Context, provider llm.Provider, topic string, publishedTitles []string) (string, bool) {
	if len(publishedTitles) == 0 {
		return "", false
	}

	// 1. Fast-path: case-insensitive direct, substring, or keyword overlap match
	topicClean := strings.ToLower(strings.TrimSpace(cleanSearchOperators(topic)))
	topicKeywords := extractKeywords(topic)

	for _, title := range publishedTitles {
		titleClean := strings.ToLower(strings.TrimSpace(cleanSearchOperators(title)))

		// Substring check
		if topicClean == titleClean || strings.Contains(titleClean, topicClean) || strings.Contains(topicClean, titleClean) {
			return title, true
		}

		// Keyword overlap check (if >= 2 common keywords or >= 50% overlap ratio)
		titleKeywords := extractKeywords(title)
		commonCount, ratio := calculateOverlap(topicKeywords, titleKeywords)
		if commonCount >= 3 || (commonCount >= 2 && ratio >= 0.5) {
			fmt.Printf("[IsTopicTooSimilar] Fast-path overlap match: '%s' vs '%s' (communs: %d, ratio: %.2f)\n", topic, title, commonCount, ratio)
			return title, true
		}
	}

	// 2. Semantic comparison using LLM
	if provider == nil {
		return "", false
	}

	titlesBlock := ""
	for i, t := range publishedTitles {
		titlesBlock += fmt.Sprintf("%d. %s\n", i+1, t)
	}

	systemPrompt := `Tu es un assistant éditorial critique. Ton rôle est de vérifier si un nouveau sujet d'article proposé est sémantiquement trop proche d'un article déjà publié dans la liste fournie.
Nous voulons éviter de publier des articles redondants ou traitant exactement du même sujet sous le même angle.

ARTICLES DÉJÀ PUBLIÉS :
` + titlesBlock + `

NOUVEAU SUJET PROPOSÉ :
"%s"

Consignes de décision :
- Si le nouveau sujet est sémantiquement équivalent ou très similaire à un article de la liste, réponds au format JSON suivant :
{
  "similar": true,
  "matched_title": "[Titre exact de l'article similaire de la liste]"
}
- Si le nouveau sujet est distinct, original ou aborde un aspect totalement différent, réponds :
{
  "similar": false,
  "matched_title": ""
}

Réponds UNIQUEMENT avec l'objet JSON strict sans fioritures ni balises markdown.`

	prompt := fmt.Sprintf(systemPrompt, topic)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Évalue la similarité du sujet."},
	}

	res, err := provider.Generate(ctx, messages)
	if err != nil {
		return "", false
	}

	res = strings.TrimSpace(res)
	if strings.HasPrefix(res, "```json") {
		res = strings.TrimPrefix(res, "```json")
		res = strings.TrimSuffix(strings.TrimSpace(res), "```")
	} else if strings.HasPrefix(res, "```") {
		res = strings.TrimPrefix(res, "```")
		res = strings.TrimSuffix(strings.TrimSpace(res), "```")
	}
	res = strings.TrimSpace(res)

	var decision struct {
		Similar      bool   `json:"similar"`
		MatchedTitle string `json:"matched_title"`
	}
	if err := json.Unmarshal([]byte(res), &decision); err == nil {
		return decision.MatchedTitle, decision.Similar
	}

	// Fallback parsing if JSON unmarshal failed
	resLower := strings.ToLower(res)
	if strings.Contains(resLower, "\"similar\": true") || strings.Contains(resLower, "\"similar\":true") {
		for _, title := range publishedTitles {
			if strings.Contains(resLower, strings.ToLower(title)) {
				return title, true
			}
		}
		return publishedTitles[0], true
	}

	return "", false
}
