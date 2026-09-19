package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/scheduler"
)

// ReviewerAgent specializes in proofreading, formatting validation, and quality enhancement of article drafts.
type ReviewerAgent struct {
	provider llm.Provider
}

// NewReviewerAgent creates a new ReviewerAgent.
func NewReviewerAgent(provider llm.Provider) *ReviewerAgent {
	return &ReviewerAgent{
		provider: provider,
	}
}

// ReviewAuditPrompt is the system prompt directing the ReviewerAgent to validate and refine article drafts.
const ReviewAuditPrompt = `Tu es l'Agent Relecteur et Contrôleur Qualité en Chef de Pixel.
Ton rôle est d'auditer avec rigueur, corriger et valider le contenu d'un article en cours de préparation avant sa publication sur AppliYou.

Voici l'article soumis :
TITRE : "%s"
SUJET / THÉMATIQUE : "%s"
MOTS-CLÉS UNSPLASH : "%s"

CONTENU HTML BRUT :
%s

CRITÈRES STRICTS D'ÉVALUATION ET D'ENRICHISSEMENT :
1. STRUCTURE & FORMATAGE HTML PUR (AUCUN MARKDOWN BRUT ACCEPTÉ) :
   - Assure-toi que le texte est intégralement converti en HTML valide. AUCUN dièse (# ou ## ou ###) ne doit subsister : remplace-les systématiquement par des balises <h2> et <h3>.
   - Les listes en étoiles (* ou -) doivent être converties en <ul><li>...</li></ul> ou <ol><li>...</li></ol>.
   - Le gras Markdown (**texte**) doit être converti en <strong>texte</strong>.
   - Chaque paragraphe doit être enveloppé dans une balise <p>...</p>.
   - Les formules LaTeX ($$...$$) doivent être transcrites en texte clair ou balises <code> lisibles.
2. TABLEAU HTML OBLIGATOIRE :
   - L'article DOIT impérativement comporter au moins un tableau HTML complet (<table>, <thead>, <tbody>, <tr>, <th>, <td>) résumant des données, comparant des solutions ou synthétisant les notions clés.
   - SI LE TABLEAU EST ABSENT OU TRONQUÉ : Tu DOIS obligatoirement concevoir un tableau complet et riche et l'insérer dans le texte.
3. BLOCS DE CODE OBLIGATOIRES (pour sujets tech/informatique/sciences) :
   - Si le sujet concerne Linux, le dev, le cloud, le DevOps, l'IA ou les systèmes, intègre ou affine un ou plusieurs blocs de code formatés avec <pre><code class="language-...">...</code></pre>.
4. RÉPARATION DES TRONCATURES ET DES FINS COUPÉES :
   - Si l'article s'arrête brusquement (phrase inachevée, balise "<" non fermée, tableau annoncé mais non rédigé), tu DOIS obligatoirement poursuivre et compléter la rédaction jusqu'à une véritable conclusion aboutie.
5. MOTS-CLÉS VISUELS UNSPLASH :
   - Vérifie que le mot-clé visuel est en ANGLAIS, précis et adapté à la recherche d'une photo réaliste sur Unsplash (ex: "datacenter server rack", "neural brain computing").
6. PROPRETÉ & STYLE :
   - Corrige les coquilles orthographiques ou grammaticales.
   - Supprime toute mention méta ou de politesse (ex: "Voici l'article relu").

Formatte OBLIGATOIREMENT ta réponse selon ce schéma strict :

---REVIEW_NOTES---
[Résumé des vérifications effectuées et améliorations apportées en 2 ou 3 phrases]

---REVIEWED_TITLE---
[Titre final validé ou optimisé]

---REVIEWED_KEYWORDS---
[Mots-clés Unsplash en anglais validés]

---REVIEWED_CONTENT---
[Contenu HTML complet, enrichi, corrigé et prêt à publier]
`

// Review performs automated proofreading and validation on an ArticleDraft.
// Implements scheduler.ArticleReviewer interface.
func (r *ReviewerAgent) Review(ctx context.Context, draft *scheduler.ArticleDraft) (reviewedTitle, reviewedKeywords, reviewedContent, reviewNotes string, err error) {
	if draft == nil {
		return "", "", "", "", fmt.Errorf("draft is nil")
	}

	contentToReview := draft.RawContent
	if len(strings.TrimSpace(contentToReview)) == 0 {
		return "", "", "", "", fmt.Errorf("draft content is empty")
	}

	// If no provider is available, execute fallback algorithmic validation
	if r.provider == nil {
		return r.fallbackValidation(draft)
	}

	prompt := fmt.Sprintf(ReviewAuditPrompt, draft.Title, draft.Topic, draft.Keywords, contentToReview)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "Tu es un réviseur et relecteur professionnel de haute exigence éditoriale."},
		{Role: llm.RoleUser, Content: prompt},
	}

	res, errGen := r.provider.Generate(ctx, messages)
	if errGen != nil {
		// Fallback to algorithmic check if LLM generation fails
		notes := fmt.Sprintf("Validation algorithmique de repli (échec LLM: %v)", errGen)
		rTitle, rKey, rCont, _, errFallback := r.fallbackValidation(draft)
		return rTitle, rKey, rCont, notes, errFallback
	}

	// Parse review output delimiters
	parsedTitle, parsedKeywords, parsedContent, parsedNotes := parseReviewOutput(res)

	if parsedTitle == "" {
		parsedTitle = draft.Title
	}
	if parsedKeywords == "" {
		parsedKeywords = draft.Keywords
	}
	if parsedContent == "" {
		parsedContent = contentToReview
	}
	if parsedNotes == "" {
		parsedNotes = "Relecture effectuée avec succès par le ReviewerAgent."
	}

	// Always pass through CleanToSemanticHTML to guarantee 100% pure semantic HTML without raw Markdown
	parsedContent = scheduler.CleanToSemanticHTML(parsedContent, parsedTitle)

	return parsedTitle, parsedKeywords, parsedContent, parsedNotes, nil
}

func (r *ReviewerAgent) fallbackValidation(draft *scheduler.ArticleDraft) (string, string, string, string, error) {
	content := draft.RawContent
	var notes []string

	if len(content) < 500 {
		return "", "", "", "", fmt.Errorf("article trop court pour la validation (%d caractères)", len(content))
	}

	content = scheduler.CleanToSemanticHTML(content, draft.Title)
	notes = append(notes, "Formatage HTML sémantique pur validé")

	return draft.Title, draft.Keywords, content, strings.Join(notes, "; "), nil
}

func parseReviewOutput(output string) (title, keywords, content, notes string) {
	// Extract notes
	notes = extractBetweenDelimiters(output, "---REVIEW_NOTES---", "---REVIEWED_TITLE---")
	if notes == "" {
		notes = extractBetweenDelimiters(output, "---REVIEW_NOTES---", "---REVIEWED_CONTENT---")
	}

	// Extract title
	title = extractBetweenDelimiters(output, "---REVIEWED_TITLE---", "---REVIEWED_KEYWORDS---")
	if title == "" {
		title = extractBetweenDelimiters(output, "---REVIEWED_TITLE---", "---REVIEWED_CONTENT---")
	}

	// Extract keywords
	keywords = extractBetweenDelimiters(output, "---REVIEWED_KEYWORDS---", "---REVIEWED_CONTENT---")

	// Extract content
	if idx := strings.Index(output, "---REVIEWED_CONTENT---"); idx != -1 {
		content = strings.TrimSpace(output[idx+len("---REVIEWED_CONTENT---"):])
	}

	title = strings.TrimSpace(cleanMarkdownWraps(title))
	keywords = strings.TrimSpace(cleanMarkdownWraps(keywords))
	content = strings.TrimSpace(cleanMarkdownWraps(content))
	notes = strings.TrimSpace(cleanMarkdownWraps(notes))

	return title, keywords, content, notes
}

func extractBetweenDelimiters(text, startDelim, endDelim string) string {
	startIdx := strings.Index(text, startDelim)
	if startIdx == -1 {
		return ""
	}
	startIdx += len(startDelim)

	rest := text[startIdx:]
	endIdx := strings.Index(rest, endDelim)
	if endIdx == -1 {
		return strings.TrimSpace(rest)
	}

	return strings.TrimSpace(rest[:endIdx])
}
