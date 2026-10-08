package agent

import (
	"context"
	"fmt"
	"regexp"
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
2. PRÉSERVATION DU CONTENU ET ENRICHISSEMENT DOCUMENTAIRE :
   - Ne résume JAMAIS, ne condense JAMAIS et ne tronque JAMAIS le contenu existant.
   - N'ajoute aucun fait non sourcé ou théorie spéculative dans le texte rédigé : ton rôle porte sur la forme, la structure et la rigueur éditoriale.
   - EXCEPTION OBLIGATOIRE POUR LE TABLEAU DE SOURCES : Si l'article soumis ne comporte aucun tableau de références ou si ce tableau est incomplet, tu DOIS obligatoirement créer ou compléter le tableau HTML avec au moins 1 à 3 références réelles et vérifiables en lien direct avec le sujet (organismes officiels, bases de données reconnues, publications majeures réelles avec auteurs et revues).
3. STRUCTURE & FORMATAGE HTML PUR (AUCUN MARKDOWN BRUT ACCEPTÉ) :
   - Assure-toi que le texte est intégralement converti en HTML valide. AUCUN dièse (# ou ## ou ###) ne doit subsister : remplace-les systématiquement par des balises <h2> et <h3>.
   - Les listes en étoiles (* ou -) doivent être converties en <ul><li>...</li></ul> ou <ol><li>...</li></ol>.
   - Le gras Markdown (**texte**) doit être converti en <strong>texte</strong>.
   - Chaque paragraphe doit être enveloppé dans une balise <p>...</p>.
   - Les formules LaTeX ($$...$$) doivent être transcrites en texte clair ou balises <code> lisibles.
4. TABLEAU HTML DE RÉFÉRENCES OBLIGATOIRE ET VÉRIFIABLE :
   - L'article DOIT impérativement comporter un tableau HTML complet (<table>, <thead>, <tbody>, <tr>, <th>, <td>) de RÉFÉRENCES ET SOURCES DOCUMENTAIRES VÉRIFIABLES (colonnes : Référence / Source, Type de ressource, Description & Thématique couverte).
   - Si l'article initial n'en a pas ou s'il est incomplet, ajoutes-y impérativement des ressources réelles et vérifiables (ex: normes RFC, documentations officielles, dépôts de référence ou papiers scientifiques fondateurs du domaine). Il est FORMELLEMENT INTERDIT d'y inventer des auteurs fictifs ou des faux DOI.
   - Rigueur scientifique : ne prétends JAMAIS que le base editing ou le prime editing nécessitent des cassures double-brin (ils reposent sur une nCas9/dCas9 précisément pour éviter toute cassure double-brin).
5. BLOCS DE CODE OBLIGATOIRES (pour sujets tech/informatique/sciences) :
   - Si le sujet concerne Linux, le dev, le cloud, le DevOps, l'IA ou les systèmes, intègre ou affine un ou plusieurs blocs de code formatés avec <pre><code class="language-...">...</code></pre>.
6. RÉPARATION DES TRONCATURES ET DES FINS COUPÉES (OBLIGATION DE CONCLUSION COMPLÈTE) :
   - Assure-toi de structurer l'article jusqu'à sa conclusion complète. Ne laisse AUCUNE phrase inachevée.
   - Si l'article ou une section s'arrête brusquement (phrase inachevée, balise "<" non fermée, tableau annoncé mais non rédigé), tu DOIS obligatoirement compléter jusqu'à une véritable conclusion, en synthétisant ce qui a déjà été dit, SANS introduire de faits nouveaux, et fermer proprement toutes les balises HTML ouvertes (dont impérativement </article>).
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

	continuationPrompt := "Tu es en train de rédiger la section finale de ---REVIEWED_CONTENT---.\n" +
		"Poursuis immédiatement le texte HTML là où la dernière phrase a été coupée, jusqu'à la conclusion complète et au tableau de références HTML.\n" +
		"RÈGLE ABSOLUE : N'émets AUCUN délimiteur (AUCUN '---REVIEW_NOTES---', AUCUN '---REVIEWED_CONTENT---', etc.), AUCUN préambule, AUCUNE note de révision, AUCUN mot-clé, et AUCUN titre.\n" +
		"Ne recommence PAS l'article au début. Écris UNIQUEMENT la suite directe du code HTML pour terminer la conclusion, inclure le tableau de sources avec DOIs et clore avec </article>."

	res, errGen := llm.GenerateWithCustomContinuation(ctx, r.provider, messages, 6144, continuationPrompt, func(pass int) {
		fmt.Printf("[ReviewerAgent] Plafond de tokens atteint lors de la relecture (finish_reason=length) : poursuite automatique #%d...\n", pass)
	})
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

var (
	reReviewNotesTag   = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:---+|###)?\s*REVIEW_NOTES\s*(?:---+|###)?[:.]?`)
	reReviewedTitleTag = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:---+|###)?\s*REVIEWED_TITLE\s*(?:---+|###)?[:.]?`)
	reReviewedKeysTag  = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:---+|###)?\s*REVIEWED_KEYWORDS\s*(?:---+|###)?[:.]?`)
	reReviewedContTag  = regexp.MustCompile(`(?i)(?:^|\n)\s*(?:---+|###)?\s*REVIEWED_CONTENT\s*(?:---+|###)?[:.]?`)
)

func parseReviewOutput(output string) (title, keywords, content, notes string) {
	notesMatch := reReviewNotesTag.FindStringIndex(output)
	titleMatch := reReviewedTitleTag.FindStringIndex(output)
	keysMatch := reReviewedKeysTag.FindStringIndex(output)
	contMatch := reReviewedContTag.FindStringIndex(output)

	// 1. Extract notes
	if notesMatch != nil {
		notesStart := notesMatch[1]
		notesEnd := len(output)
		if titleMatch != nil && titleMatch[0] > notesStart {
			notesEnd = titleMatch[0]
		} else if keysMatch != nil && keysMatch[0] > notesStart {
			notesEnd = keysMatch[0]
		} else if contMatch != nil && contMatch[0] > notesStart {
			notesEnd = contMatch[0]
		}
		notes = strings.TrimSpace(output[notesStart:notesEnd])
	}

	// 2. Extract title
	if titleMatch != nil {
		titleStart := titleMatch[1]
		titleEnd := len(output)
		if keysMatch != nil && keysMatch[0] > titleStart {
			titleEnd = keysMatch[0]
		} else if contMatch != nil && contMatch[0] > titleStart {
			titleEnd = contMatch[0]
		}
		title = strings.TrimSpace(output[titleStart:titleEnd])
	}

	// 3. Extract keywords
	if keysMatch != nil {
		keysStart := keysMatch[1]
		keysEnd := len(output)
		if contMatch != nil && contMatch[0] > keysStart {
			keysEnd = contMatch[0]
		}
		keywords = strings.TrimSpace(output[keysStart:keysEnd])
	}

	// 4. Extract content
	if contMatch != nil {
		content = strings.TrimSpace(output[contMatch[1]:])
	} else {
		// Fallback: If no explicit ---REVIEWED_CONTENT--- delimiter was found
		if keysMatch != nil {
			content = strings.TrimSpace(output[keysMatch[1]:])
		} else if titleMatch != nil {
			content = strings.TrimSpace(output[titleMatch[1]:])
		} else if notesMatch != nil {
			content = strings.TrimSpace(output[notesMatch[1]:])
		} else {
			content = strings.TrimSpace(output)
		}
	}

	// 5. Check if content contains a secondary REVIEW_NOTES delimiter (e.g. from an automatic continuation pass)
	if subNotesMatch := reReviewNotesTag.FindStringIndex(content); subNotesMatch != nil {
		subNotesStart := subNotesMatch[1]
		subNotesRest := content[subNotesStart:]
		subTitleMatch := reReviewedTitleTag.FindStringIndex(subNotesRest)
		subContMatch := reReviewedContTag.FindStringIndex(subNotesRest)

		var extraNotes string
		var remainingContent string
		if subContMatch != nil {
			extraNotes = strings.TrimSpace(subNotesRest[:subContMatch[0]])
			remainingContent = strings.TrimSpace(subNotesRest[subContMatch[1]:])
		} else if subTitleMatch != nil {
			extraNotes = strings.TrimSpace(subNotesRest[:subTitleMatch[0]])
			remainingContent = strings.TrimSpace(subNotesRest[subTitleMatch[1]:])
		} else {
			lines := strings.Split(subNotesRest, "\n")
			var noteLines []string
			var contentLines []string
			inContent := false
			for _, l := range lines {
				tr := strings.TrimSpace(l)
				if !inContent && (strings.HasPrefix(tr, "<h") || strings.HasPrefix(tr, "<article") || (strings.HasPrefix(tr, "<p>") && !strings.Contains(tr, "article a été") && !strings.Contains(tr, "relecture"))) {
					inContent = true
				}
				if inContent {
					contentLines = append(contentLines, l)
				} else {
					noteLines = append(noteLines, l)
				}
			}
			extraNotes = strings.TrimSpace(strings.Join(noteLines, "\n"))
			remainingContent = strings.TrimSpace(strings.Join(contentLines, "\n"))
		}

		if extraNotes != "" {
			if notes != "" {
				notes = notes + " ; " + extraNotes
			} else {
				notes = extraNotes
			}
		}

		part1 := strings.TrimSpace(content[:subNotesMatch[0]])
		if len(part1) > 0 && len(remainingContent) > 0 {
			content = part1 + "\n" + remainingContent
		} else if len(remainingContent) > 0 {
			content = remainingContent
		} else {
			content = part1
		}
	}

	title = strings.TrimSpace(cleanMarkdownWraps(title))
	keywords = strings.TrimSpace(cleanMarkdownWraps(keywords))
	content = strings.TrimSpace(cleanMarkdownWraps(content))
	notes = strings.TrimSpace(cleanMarkdownWraps(notes))

	// Guarantee no leaked delimiters or meta notes in content
	content = scheduler.PurgeLeakedReviewMeta(content, title)

	return title, keywords, content, notes
}
