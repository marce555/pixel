package agent

import (
	"regexp"
	"strings"

	"github.com/marce555/pixel/internal/scheduler"
)

// ArticleGenerationPromptTemplate is the common prompt template used by Pixel agents to write articles.
const ArticleGenerationPromptTemplate = `Tu es le Rédacteur en Chef de Pixel.
Ton rôle est de rédiger un article de blog haut de gamme, complet, captivant, très fouillé et extrêmement détaillé (format long et approfondi : entre 1800 et 2500 mots / 10 000 à 14 000 caractères) optimisé pour le SEO en français sur le sujet suivant : "%s".

Voici les informations et le contexte récupérés à ce sujet :
%s

RÈGLES IMPÉRATIVES DE RÉDACTION ET DE STRUCTURE :
1. EXPANSION ET PROFONDEUR : L'article doit être LONG, RICHE et EXHAUSTIF. Développe chaque concept en profondeur avec des explications concrètes, des cas d'usage réels, des exemples techniques et des analyses de fond. Ne rédige JAMAIS un résumé rapide.
2. STRUCTURE ÉDITORIALE DÉTAILLÉE :
   - Une balise racine <article class="blog-article">...</article> englobant tout le contenu.
   - Une introduction immersive posant la problématique, le contexte et les enjeux clés.
   - Entre 5 et 7 grandes sections structurées avec des titres <h2>.
   - Sous chaque grande section, 2 à 3 sous-sections substantielles avec des sous-titres <h3> pour traiter les aspects techniques ou méthodologiques.
   - Une conclusion prospective et analytique résumant les perspectives d'avenir.
3. FORMATAGE STRICTEMENT EN HTML SÉMANTIQUE PUR (INTERDICTION ABSOLUE DU MARKDOWN) :
   - N'utilise AUCUNE syntaxe Markdown (JAMAIS de '#', '##', '###', JAMAIS de '**', JAMAIS de '*' ou '-' pour les listes, JAMAIS de '$$').
   - Rédige et formate le contenu EXCLUSIVEMENT en HTML sémantique propre avec :
     * Des balises <p> obligatoires pour TOUS les paragraphes sans exception.
     * Des balises <strong> pour mettre en valeur les termes clés.
     * Des listes à puces <ul><li>...</li></ul> ou numérotées <ol><li>...</li></ol> pour aérer la lecture.
     * RÈGLE OBLIGATOIRE : Au moins un TABLEAU HTML complet (<table>, <thead>, <tbody>, <tr>, <th>, <td>) résumant des données chiffrées, comparant des solutions ou synthétisant les points clés.
     * RÈGLE OBLIGATOIRE (si sujet technique/info/sciences) : Au moins un ou plusieurs blocs de code HTML complets formatés avec <pre><code class="language-...">...</code></pre> (ex: language-bash, language-python, language-json, language-yaml).
4. Ne mets AUCUNE formule de politesse du type "Voici l'article", commence directement avec les délimiteurs ci-dessous.

Formatte ta réponse EXACTEMENT avec la structure suivante :

---TITLE---
[Titre accrocheur, précis et professionnel de l'article]

---KEYWORDS---
[Un SEUL mot clé visuel très pertinent en anglais (ex: cybersecurity, devops, battery, quantum, cloud, server) pour chercher l'image d'illustration sur Unsplash]

---CONTENT---
[Le contenu HTML complet, riche et structuré de l'article enveloppé dans <article class="blog-article"> sans aucun caractère Markdown]
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
