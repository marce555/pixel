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

CRITÈRES STRICTS D'ÉVALUATION, DE RIGUEUR ET D'AUDIT FACTUEL :
1. AUDIT FACTUEL ET INTERDICTION STRICTE DES HALLUCINATIONS :
   - Vérifie rigoureusement la vérificabilité de toutes les références, de tous les exemples et des cas d'étude cités dans l'article.
   - INTERDICTION FORMELLE d'extrapoler ou d'inventer des noms de projets, d'organismes, d'équipes de recherche, de chercheurs, de publications ou d'exemples fictifs.
   - Si une donnée factuelle ou une étude spécifique citée dans le texte paraît inventée ou invérifiable, remplace-la par la description générale du principe ou du mécanisme technique/scientifique sans citer de projet fictif.
2. PRÉSERVATION DE LA RICHESSE ET DU FORMAT LONG (10 000 À 14 000 CARACTÈRES) :
   - L'article doit être approfondi, fouillé et substantiel (entre 1800 et 2500 mots). Ne résume JAMAIS, ne condense JAMAIS et ne tronque JAMAIS le contenu.
   - Si le brouillon soumis manque de détails ou de sections, enrichis chaque grande partie avec des explications techniques détaillées, des cas d'usage concrets et des approfondissements conceptuels avérés.
3. STRUCTURE & FORMATAGE HTML PUR (AUCUN MARKDOWN BRUT ACCEPTÉ) :
   - Assure-toi que le texte est intégralement converti en HTML valide. AUCUN dièse (# ou ## ou ###) ne doit subsister : remplace-les systématiquement par des balises <h2> et <h3>.
   - Les listes en étoiles (* ou -) doivent être converties en <ul><li>...</li></ul> ou <ol><li>...</li></ol>.
   - Le gras Markdown (**texte**) doit être converti en <strong>texte</strong>.
   - Chaque paragraphe doit être enveloppé dans une balise <p>...</p>.
   - Les formules LaTeX ($$...$$) doivent être transcrites en texte clair ou balises <code> lisibles.
4. TABLEAU HTML DE RÉFÉRENCES OBLIGATOIRE ET VÉRIFIABLE :
   - L'article DOIT impérativement comporter un tableau HTML complet (<table>, <thead>, <tbody>, <tr>, <th>, <td>) de RÉFÉRENCES ET SOURCES DOCUMENTAIRES VÉRIFIABLES (colonnes : Référence / Source, Type de ressource, Description & Thématique couverte).
   - Le tableau doit comporter EXCLUSIVEMENT des ressources réelles et vérifiables en lien direct avec le sujet (ex: normes RFC, documentations officielles, dépôts de référence ou papiers scientifiques du domaine). Il est FORMELLEMENT INTERDIT d'y injecter des ressources ou des notions fictives.
5. BLOCS DE CODE OBLIGATOIRES (pour sujets tech/informatique/sciences) :
   - Si le sujet concerne Linux, le dev, le cloud, le DevOps, l'IA ou les systèmes, intègre ou affine un ou plusieurs blocs de code formatés avec <pre><code class="language-...">...</code></pre>.
6. RÉPARATION DES TRONCATURES ET DES FINS COUPÉES :
   - Si l'article s'arrête brusquement (phrase inachevée, balise "<" non fermée, tableau annoncé mais non rédigé), tu DOIS obligatoirement poursuivre et compléter la rédaction jusqu'à une véritable conclusion aboutie.
7. MOTS-CLÉS VISUELS UNSPLASH :
   - Vérifie que le mot-clé visuel est en ANGLAIS, précis et adapté à la recherche d'une photo réaliste sur Unsplash (ex: "datacenter server rack", "neural brain computing").
8. PROPRETÉ & STYLE :
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

	res, errGen := llm.GenerateWithTokens(ctx, r.provider, messages, 4096)
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
