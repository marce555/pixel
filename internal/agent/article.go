package agent

import (
	"regexp"
	"strings"

	"github.com/marce555/pixel/internal/scheduler"
)

// ArticleGenerationPromptTemplate is the common prompt template used by Pixel agents to write articles.
const ArticleGenerationPromptTemplate = `Tu es le Rédacteur en Chef de Pixel.
Ton rôle est de rédiger un article de blog haut de gamme, complet, captivant, très fouillé et extrêmement détaillé (au moins 1500 mots / 4000 caractères) optimisé pour le SEO en français sur le sujet suivant : "%s".

Voici les informations et le contexte récupérés à ce sujet :
%s

RÈGLES IMPÉRATIVES DE RÉDACTION ET DE STRUCTURE :
1. EXPANSION ET PROFONDEUR : L'article doit être LONG, RICHE et EXHAUSTIF. Développe chaque concept en profondeur avec des explications concrètes, des cas d'usage réels, des exemples techniques et des analyses de fond. Ne rédige JAMAIS un résumé rapide.
2. FORMATAGE STRICTEMENT EN HTML SÉMANTIQUE PUR (INTERDICTION ABSOLUE DU MARKDOWN) :
   - N'utilise AUCUNE syntaxe Markdown (JAMAIS de '#', '##', '###', JAMAIS de '**', JAMAIS de '*' ou '-' pour les listes, JAMAIS de '$$').
   - Rédige et formate le contenu EXCLUSIVEMENT en HTML sémantique propre avec :
     * Une balise globale <article class="blog-post">...</article> englobant tout le contenu.
     * Des titres <h2> pour chaque grande section et <h3> pour chaque sous-section.
     * Des balises <p> obligatoires pour TOUS les paragraphes sans exception.
     * Des balises <strong> pour mettre en valeur les termes clés.
     * Des listes à puces <ul><li>...</li></ul> ou numérotées <ol><li>...</li></ol>.
     * RÈGLE OBLIGATOIRE : Au moins un TABLEAU HTML complet (<table>, <thead>, <tbody>, <tr>, <th>, <td>) résumant des données, comparant des solutions ou synthétisant les points clés.
     * RÈGLE OBLIGATOIRE (si sujet technique/info/sciences) : Au moins un ou plusieurs blocs de code HTML complets formatés avec <pre><code class="language-...">...</code></pre> (ex: language-bash, language-python, language-json).
3. Ne mets AUCUNE formule de politesse du type "Voici l'article", commence directement avec les délimiteurs ci-dessous.

Formatte ta réponse EXACTEMENT avec la structure suivante :

---TITLE---
[Titre accrocheur, précis et professionnel de l'article]

---KEYWORDS---
[Un SEUL mot clé visuel très pertinent en anglais (ex: cybersecurity, devops, battery, quantum, cloud, server) pour chercher l'image d'illustration sur Unsplash]

---CONTENT---
[Le contenu HTML sémantique pur (<article>, <h2>, <p>, <strong>, <ul>, <table>, etc.) sans aucun caractère Markdown]
`

// CleanTopic removes search query operators, quotes, and cleans spaces from topics/titles.
func CleanTopic(text string) string {
	clean := text
	// Remove site:..., filetype:..., ext:...
	reOps := regexp.MustCompile(`(?i)\b(site|filetype|ext):\S+`)
	clean = reOps.ReplaceAllString(clean, "")

	clean = strings.ReplaceAll(clean, `"`, "")
	clean = strings.TrimSpace(clean)

	words := strings.Fields(clean)
	return strings.Join(words, " ")
}

// ParseDelimitedArticle parses the LLM output separated by ---TITLE---, ---KEYWORDS---, and ---CONTENT---.
func ParseDelimitedArticle(output string) (string, string, string) {
	var title, keywords, content string

	// 1. Extract markers robustly without splitting on markdown dividers
	title = extractBetweenMarkers(output, "---TITLE---", "---KEYWORDS---")
	if title == "" {
		title = extractBetweenMarkers(output, "---TITLE---", "---CONTENT---")
	}

	keywords = extractBetweenMarkers(output, "---KEYWORDS---", "---CONTENT---")
	if keywords == "" {
		keywords = extractBetweenMarkers(output, "---KEYWORDS---", "---TITLE---")
	}

	content = extractFromMarkerToEnd(output, "---CONTENT---")

	// Fallback split if exact markers were slightly altered by LLM (e.g. --- TITLE ---)
	if content == "" {
		parts := strings.Split(output, "---")
		for i, part := range parts {
			trimmed := strings.TrimSpace(part)
			lowerTrimmed := strings.ToLower(trimmed)
			if strings.HasPrefix(lowerTrimmed, "title") {
				t := strings.TrimPrefix(trimmed[5:], ":")
				title = strings.TrimSpace(t)
			} else if strings.HasPrefix(lowerTrimmed, "keywords") {
				k := strings.TrimPrefix(trimmed[8:], ":")
				keywords = strings.TrimSpace(k)
			} else if strings.HasPrefix(lowerTrimmed, "content") {
				// Re-join remaining parts in case article content contains '---' horizontal rules!
				c := strings.TrimPrefix(trimmed[7:], ":")
				content = strings.TrimSpace(c)
				if i+1 < len(parts) {
					content += "\n---" + strings.Join(parts[i+1:], "---")
				}
				break
			}
		}
	}

	// Clean up markdown block wraps if the LLM outputted them inside delimiters
	title = cleanMarkdownWraps(title)
	keywords = cleanMarkdownWraps(keywords)
	content = cleanMarkdownWraps(content)

	if title != "" {
		title = CleanTopic(title)
	}

	if content != "" {
		content = scheduler.CleanToSemanticHTML(content, title)
	}

	return title, keywords, content
}

func extractBetweenMarkers(text, startMarker, endMarker string) string {
	lowerText := strings.ToLower(text)
	startIdx := strings.Index(lowerText, strings.ToLower(startMarker))
	if startIdx == -1 {
		return ""
	}
	startIdx += len(startMarker)

	endIdx := strings.Index(lowerText[startIdx:], strings.ToLower(endMarker))
	if endIdx == -1 {
		return strings.TrimSpace(text[startIdx:])
	}
	return strings.TrimSpace(text[startIdx : startIdx+endIdx])
}

func extractFromMarkerToEnd(text, marker string) string {
	lowerText := strings.ToLower(text)
	idx := strings.Index(lowerText, strings.ToLower(marker))
	if idx == -1 {
		return ""
	}
	return strings.TrimSpace(text[idx+len(marker):])
}

func cleanMarkdownWraps(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 2 {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		} else {
			text = strings.Trim(text, "`")
		}
	}
	return strings.TrimSpace(text)
}
