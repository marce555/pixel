package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/marce555/pixel/internal/citations"
	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/scheduler"
)

// BibliographicAuditReport is an alias for scheduler.BibliographicAuditReport.
type BibliographicAuditReport = scheduler.BibliographicAuditReport

// BibliographicAuditorAgent conducts an explicit, specialized audit of bibliographic references,
// author co-signatures (detecting AI chimeras), discovery dates, and laboratory/university affiliations.
type BibliographicAuditorAgent struct {
	provider          llm.Provider
	searcher          FactSearcher
	citationValidator *citations.Validator
}

// NewBibliographicAuditorAgent creates a new BibliographicAuditorAgent.
func NewBibliographicAuditorAgent(provider llm.Provider, searcher FactSearcher) *BibliographicAuditorAgent {
	return &BibliographicAuditorAgent{
		provider:          provider,
		searcher:          searcher,
		citationValidator: citations.NewValidator(),
	}
}

// SetCitationValidator overrides the citations validator (for testing).
func (b *BibliographicAuditorAgent) SetCitationValidator(v *citations.Validator) {
	b.citationValidator = v
}

const bibliographicSystemPrompt = `Tu es l'Agent Vérificateur de Police Bibliographique et des Affiliations Académiques.
Ton rôle est d'inspecter impitoyablement les citations de chercheurs, leurs affiliations (laboratoires, universités), les dates de découvertes et les références documentaires d'un article pour éliminer toutes les chimères statistiques créées par les LLMs.

RÈGLES DE CONTRÔLE CRITIQUE :

1. DÉTECTION DES CHIMÈRES BIBLIOGRAPHIQUES :
- Repère les mariages artificiels : des LLMs associent souvent deux sommités du domaine comme co-auteurs fictifs (ex. Yamanaka et Thomson), ou attribuent un sujet réel à un auteur aléatoire dans une revue majeure (Nature, Science, Cell).
- Vérifie si les dates correspondent aux découvertes réelles (ex. la découverte des iPSC humaines date de 2007, pas de 2014).

2. CONTRÔLE DES AFFILIATIONS ET DES LABORATOIRES :
- Vérifie l'adéquation chercheur / laboratoire / université (ex. David Baker est à l'Université de Washington / HHMI, pas à Stanford ; Stanley Qi est à Stanford ; James Thomson est à l'Université du Wisconsin à Madison / Morgridge Institute, pas à Londres ou au Michigan).
- Pour les institutions japonaises : Kyoto University se nomme strictement en français « Université de Kyoto » (ou CiRA - Center for iPS Cell Research and Application), JAMAIS « Université du Japon à Kyoto » ni « Université des Sciences du Japon ». Shinya Yamanaka dirige le CiRA à l'Université de Kyoto ; Masayo Takahashi a conduit les premiers essais cliniques de thérapie rétinienne (DMLA) au RIKEN Center for Developmental Biology (Kobe) en collaboration avec l'Université de Kyoto.
- Repère les bases de données ou outils attribués au mauvais laboratoire ou inventés de toutes pièces.
- Pour les thérapies et promoteurs industriels : repère les faux noms d'entreprises inventés par amalgame avec le nom d'un médicament (ex. « ExaCell Therapeutics » est une invention, le médicament est « exa-cel / Casgevy », développé par « Vertex Pharmaceuticals » et « CRISPR Therapeutics »).

3. POLICE DU « DOI / TITRE EXACT » :
- Si une référence n'a pas de titre précis, de volume, ou ressemble à un gabarit générique du type « Auteur, A. (Année) Nature », considère-la comme suspecte par défaut.
- Ne tolère aucune référence « plausible mais inventée ».

MODES D'ACTION & TRAITEMENT :

Pour chaque référence ou passage examiné, applique l'une de ces 3 actions :
- "CONSERVER" : La référence / affiliation est historiquement et scientifiquement exacte (auteurs, année, revue, sujet, université).
- "CORRIGER" : L'intention fait référence à une étude réelle identifiable mais contient des erreurs (ex. scinder une fausse co-publication en deux articles fondateurs réels : Yamanaka 2007 dans Cell et Thomson 2007 dans Science ; corriger le laboratoire / l'université ; remplacer « ExaCell Therapeutics » par « Vertex Pharmaceuticals et CRISPR Therapeutics » ; corriger « Université du Japon à Kyoto » par « Université de Kyoto »).
- "SUPPRIMER / REMPLACER" : La référence est une pure chimère statistique inventée par le générateur. Remplace-la soit par la revue de référence consensuelle du domaine, soit par l'organisme officiel de référence (ex. GIEC, NIH, consensus scientifique établi), soit supprime-la purement et simplement si elle n'apporte aucune valeur vérifiable.

Tu réponds STRICTEMENT en JSON valide, sans aucun texte autour.`

type biblioFindingJSON struct {
	OriginalSnippet string `json:"original_snippet"` // Exact string in the HTML to replace
	Action          string `json:"action"`           // "CONSERVER", "CORRIGER", "SUPPRIMER / REMPLACER"
	Replacement     string `json:"replacement"`      // Corrected HTML string (or empty if removed)
	Reason          string `json:"reason"`           // Concise explanation of the chimera / affiliation fix
}

type biblioAuditResponseJSON struct {
	Findings []biblioFindingJSON `json:"findings"`
}

var reAcademicClues = regexp.MustCompile(`(?i)\b(laboratoire|université|university|institut|institute|chercheur|chercheurs|équipe|professeur|prix nobel|publié|nature|science|cell|pnas|doi|revue|étude menée par|travaux de|yamanaka|thomson|baker|doudna|charpentier|zhang|lander|collins|venter)\b`)

// Audit inspects the HTML article for bibliographic chimeras, false co-authors, wrong affiliations,
// and unverified references. It applies the necessary replacements directly to the content.
func (b *BibliographicAuditorAgent) Audit(ctx context.Context, title, content string, logf func(string)) (string, *BibliographicAuditReport, error) {
	if logf == nil {
		logf = func(string) {}
	}
	report := &BibliographicAuditReport{}
	if strings.TrimSpace(content) == "" {
		return content, report, nil
	}

	logf("📚 Démarrage de l'Agent d'Audit Bibliographique & des Affiliations...")

	// 0. Validation automatisée et injection de DOIs via Crossref & PubMed E-utilities
	if b.citationValidator != nil {
		validatedContent, citReport, errCit := b.citationValidator.ValidateAndSanitize(ctx, content, logf)
		if errCit == nil && citReport != nil {
			content = validatedContent
			report.ValidatedDOIs = citReport.Validated
			report.InjectedDOIs = citReport.InjectedDOIs
			report.ChimerasRejected = citReport.ChimerasRejected
			report.Institutional = citReport.Institutional
			report.Corrected += citReport.Corrected
			report.Removed += citReport.Removed
			for _, res := range citReport.Results {
				if res.Status == citations.StatusValidated && res.ValidatedDOI != "" {
					report.Actions = append(report.Actions, fmt.Sprintf("[CROSSREF/PUBMED] %s → DOI: %s", res.Citation.RawText, res.ValidatedDOI))
				} else if res.Status == citations.StatusChimera || res.Status == citations.StatusRemoved {
					report.Actions = append(report.Actions, fmt.Sprintf("[CHIMÈRE REJETÉE] %s (%s)", res.Citation.RawText, res.Reason))
				}
			}
		}
	}

	// 1. Extract reference table rows
	tableRows := extractReferenceRows(content)

	// 2. Extract paragraphs with academic / researcher / institutional mentions
	var textSnippets []string
	reParas := regexp.MustCompile(`(?is)<(?:p|li|blockquote)\b[^>]*>(.*?)</(?:p|li|blockquote)>`)
	paras := reParas.FindAllString(content, -1)
	for _, p := range paras {
		plain := htmlToPlainText(p)
		if reAcademicClues.MatchString(plain) {
			textSnippets = append(textSnippets, p)
		}
	}

	if len(tableRows) == 0 && len(textSnippets) == 0 {
		logf("📚 Aucune citation universitaire ou référence bibliographique spécifique détectée.")
		modifiedContent := b.ensureSeminalReferences(ctx, title, content, report, logf)
		return modifiedContent, report, nil
	}

	logf(fmt.Sprintf("📚 Analyse critique : %d référence(s) en tableau et %d passage(s) académique(s) ciblés.", len(tableRows), len(textSnippets)))

	// 3. Collect targeted web evidence for cited entities
	var evidenceBuilder strings.Builder
	var visitedMu sync.Mutex
	visited := map[string]bool{}

	searchEntities := func(query string) {
		query = strings.TrimSpace(query)
		if query == "" || len(query) < 4 {
			return
		}
		visitedMu.Lock()
		if visited[query] {
			visitedMu.Unlock()
			return
		}
		visited[query] = true
		visitedMu.Unlock()

		if b.searcher != nil {
			if res, err := b.searcher.SearchWeb(query); err == nil && strings.TrimSpace(res) != "" {
				evidenceBuilder.WriteString(fmt.Sprintf("\n--- Preuves Web pour %q ---\n%s\n", query, fcTruncate(res, 2000)))
			}
		}
	}

	// Identify queries from table and text
	for _, tr := range tableRows {
		searchEntities(fcTruncate(tr.text, 80))
	}
	reNames := regexp.MustCompile(`(?i)(?:laboratoire de|travaux de|équipe de|chercheur)\s+([A-Z][a-z]+(?:\s+[A-Z][a-z]+)?)`)
	for _, snip := range textSnippets {
		matches := reNames.FindAllStringSubmatch(snip, -1)
		for _, m := range matches {
			if len(m) > 1 {
				searchEntities(m[1] + " university laboratory research")
			}
		}
	}

	evidence := evidenceBuilder.String()
	if strings.TrimSpace(evidence) == "" {
		evidence = "(Aucune source externe préalable.)"
	}

	// 4. Prompt the specialized LLM for critical evaluation
	var candidateBlocks strings.Builder
	candidateBlocks.WriteString("=== TABLEAU DES RÉFÉRENCES CÉDÉES ===\n")
	for i, tr := range tableRows {
		candidateBlocks.WriteString(fmt.Sprintf("[RÉFÉRENCE #%d] %s\n", i+1, tr.rowHTML))
	}
	candidateBlocks.WriteString("\n=== PASSAGES DU TEXTE AVEC CITATIONS D'AUTEURS/LABORATOIRES ===\n")
	for i, snip := range textSnippets {
		candidateBlocks.WriteString(fmt.Sprintf("[PASSAGE #%d] %s\n", i+1, snip))
	}

	userPrompt := fmt.Sprintf(`TITRE DE L'ARTICLE : "%s"

%s

PREUVES FACTUELLES COLLECTÉES SUR LE WEB :
"""
%s
"""

Examine chaque référence et passage selon les 3 règles de contrôle critique :
1. Détection des chimères bibliographiques (mariages d'auteurs inventés, dates anachroniques).
2. Contrôle des affiliations et laboratoires (adéquation chercheur / institut / université).
3. Police du DOI / Titre exact (interdiction formelle des références génériques ou inventées).

Pour chaque élément suspect ou nécessitant action :
Donne l'action ("CONSERVER", "CORRIGER", "SUPPRIMER / REMPLACER"), le snippet exact "original_snippet" à remplacer dans le HTML (ou l'identifiant [RÉFÉRENCE #N] / [PASSAGE #N]), son remplacement "replacement" (ou "" si suppression), et la justification "reason".
IMPORTANT : Dans "replacement", fournis EXCLUSIVEMENT le code HTML propre de remplacement (ex: <tr>...</tr> ou <p>...</p>), SANS inclure de préfixe comme [RÉFÉRENCE #...] ni [PASSAGE #...].

Réponds UNIQUEMENT sous cette forme JSON :
{
  "findings": [
    {
      "original_snippet": "...",
      "action": "CONSERVER" | "CORRIGER" | "SUPPRIMER / REMPLACER",
      "replacement": "...",
      "reason": "..."
    }
  ]
}`, title, candidateBlocks.String(), evidence)

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: bibliographicSystemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	res, err := llm.GenerateWithContinuation(ctx, b.provider, msgs, 4096, nil)
	if err != nil {
		logf(fmt.Sprintf("⚠️ Erreur lors de l'audit bibliographique : %v", err))
		return content, report, err
	}
	logf(fmt.Sprintf("  🔍 Réponse brute LLM audit bibliographique :\n%s", res))

	// 5. Parse findings
	cleanJSON := fcExtractJSON(res, '{', '}')
	var parsed biblioAuditResponseJSON
	if errJSON := json.Unmarshal([]byte(cleanJSON), &parsed); errJSON != nil {
		logf(fmt.Sprintf("⚠️ Réponse audit bibliographique non parsable : %v", errJSON))
		return content, report, nil
	}

	// Helpers for robust target identification and cleaning
	reRefNum := regexp.MustCompile(`(?i)\[\s*(?:RÉFÉRENCE|REFERENCE|REF)\s*#?(\d+)\s*\]`)
	rePassNum := regexp.MustCompile(`(?i)\[\s*PASSAGE\s*#?(\d+)\s*\]`)

	cleanSnippet := func(s string) string {
		s = strings.TrimSpace(s)
		rePrefix := regexp.MustCompile(`(?i)^\s*\[\s*(?:RÉFÉRENCE|REFERENCE|PASSAGE|REF)?\s*#?\d+\s*\]\s*`)
		s = rePrefix.ReplaceAllString(s, "")
		s = strings.TrimPrefix(s, "...")
		s = strings.TrimPrefix(s, "…")
		s = strings.TrimSuffix(s, "...")
		s = strings.TrimSuffix(s, "…")
		s = regexp.MustCompile(`(?i)(<p\b[^>]*>)\s*(?:\.\.\.|…)\s*`).ReplaceAllString(s, "$1")
		s = regexp.MustCompile(`(?i)\s*(?:\.\.\.|…)\s*(</p>)`).ReplaceAllString(s, "$1")
		return strings.TrimSpace(s)
	}

	modifiedContent := content

	resolveTarget := func(orig string) (target string, isPassage bool) {
		// 1. Direct match with exact orig
		if strings.Contains(modifiedContent, orig) {
			return orig, false
		}
		// 2. Direct match with cleaned orig
		cleaned := cleanSnippet(orig)
		if cleaned != "" && strings.Contains(modifiedContent, cleaned) {
			return cleaned, false
		}

		// 3. Match via [RÉFÉRENCE #N]
		if m := reRefNum.FindStringSubmatch(orig); len(m) > 1 {
			if idx, err := strconv.Atoi(m[1]); err == nil && idx >= 1 && idx <= len(tableRows) {
				return tableRows[idx-1].rowHTML, false
			}
		}

		// 4. Match via [PASSAGE #N]
		if m := rePassNum.FindStringSubmatch(orig); len(m) > 1 {
			if idx, err := strconv.Atoi(m[1]); err == nil && idx >= 1 && idx <= len(textSnippets) {
				return textSnippets[idx-1], true
			}
		}

		// 5. Try finding matching table row by text overlap
		plainOrig := normalizeText(htmlToPlainText(cleaned))
		if plainOrig != "" {
			for _, tr := range tableRows {
				plainRow := normalizeText(htmlToPlainText(tr.rowHTML))
				if strings.Contains(plainRow, plainOrig) || strings.Contains(plainOrig, plainRow) || wordOverlap(plainOrig, plainRow) > 0.5 {
					if strings.Contains(modifiedContent, tr.rowHTML) {
						return tr.rowHTML, false
					}
				}
			}
		}

		// 6. Try findBlock for passages
		if blk := findBlock(modifiedContent, cleaned); blk != "" {
			return blk, true
		}

		return cleaned, false
	}

	// 6. Apply replacements to content
	for _, f := range parsed.Findings {
		action := strings.ToUpper(strings.TrimSpace(f.Action))
		orig := strings.TrimSpace(f.OriginalSnippet)
		repl := cleanSnippet(f.Replacement)

		if strings.Contains(action, "CONSERV") {
			report.Conserved++
			continue
		}

		if orig == "" {
			continue
		}

		target, isPassage := resolveTarget(orig)
		if target == "" || !strings.Contains(modifiedContent, target) {
			logf(fmt.Sprintf("  ⚠️ Cible introuvable dans le HTML pour : %s", fcTruncate(orig, 50)))
			continue
		}

		if strings.Contains(action, "CORRIG") {
			if repl != "" {
				if isPassage && !strings.HasPrefix(repl, "<") {
					cleanedOrig := cleanSnippet(orig)
					if strings.Contains(target, cleanedOrig) {
						newTarget := strings.Replace(target, cleanedOrig, repl, 1)
						modifiedContent = strings.Replace(modifiedContent, target, newTarget, 1)
					} else {
						modifiedContent = strings.Replace(modifiedContent, target, repl, 1)
					}
				} else {
					modifiedContent = strings.Replace(modifiedContent, target, repl, 1)
				}
				report.Corrected++
				actionDesc := fmt.Sprintf("[CORRIGÉ] %s (%s)", fcTruncate(orig, 60), f.Reason)
				report.Actions = append(report.Actions, actionDesc)
				logf("  ✏️ " + actionDesc)
			}
		} else if strings.Contains(action, "SUPPRIM") || strings.Contains(action, "REMPLAC") {
			if repl == "" || strings.EqualFold(repl, "[SUPPRIMER]") {
				modifiedContent = strings.Replace(modifiedContent, target, "", 1)
				report.Removed++
				actionDesc := fmt.Sprintf("[SUPPRIMÉ] %s (%s)", fcTruncate(orig, 60), f.Reason)
				report.Actions = append(report.Actions, actionDesc)
				logf("  🗑️ " + actionDesc)
			} else {
				modifiedContent = strings.Replace(modifiedContent, target, repl, 1)
				report.Replaced++
				actionDesc := fmt.Sprintf("[REMPLACÉ] %s → %s (%s)", fcTruncate(orig, 50), fcTruncate(repl, 50), f.Reason)
				report.Actions = append(report.Actions, actionDesc)
				logf("  🔄 " + actionDesc)
			}
		}
	}

	// Remove empty table rows or clean incomplete tables
	modifiedContent = removeEmptyTables(modifiedContent)

	// Clean any residual institutional mistranslations that the LLM may have introduced
	modifiedContent = strings.ReplaceAll(modifiedContent, "l'Université du Japon à Kyoto", "l'Université de Kyoto (Kyoto University / CiRA)")
	modifiedContent = strings.ReplaceAll(modifiedContent, "Université du Japon à Kyoto", "Université de Kyoto")
	modifiedContent = strings.ReplaceAll(modifiedContent, "ExaCell Therapeutics", "Vertex Pharmaceuticals et CRISPR Therapeutics (promoteurs de la thérapie génique Casgevy / exa-cel)")

	// 7. Ensure at least 1-3 authentic, verified seminal references exist
	modifiedContent = b.ensureSeminalReferences(ctx, title, modifiedContent, report, logf)

	logf(fmt.Sprintf("📚 Audit bibliographique terminé : %d conservée(s), %d corrigée(s), %d remplacée(s), %d retirée(s).",
		report.Conserved, report.Corrected, report.Replaced, report.Removed))

	return modifiedContent, report, nil
}

var (
	reVerifiedDOILink = regexp.MustCompile(`(?i)<a\b[^>]*href=["']https?://(?:dx\.)?doi\.org/10\.\d{4,9}/[^"']+["'][^>]*>`)
	reRefHeading      = regexp.MustCompile(`(?i)(<h[2-4]\b[^>]*>(?:[^<]*référence[^<]*|[^<]*source[^<]*|[^<]*bibliographie[^<]*)</h[2-4]>)`)
)

type seminalCandidateJSON struct {
	Authors []string `json:"authors"`
	Year    int      `json:"year"`
	Title   string   `json:"title"`
	Journal string   `json:"journal"`
	DOI     string   `json:"doi"`
}

type seminalCandidateListJSON struct {
	Publications []seminalCandidateJSON `json:"publications"`
}

func (b *BibliographicAuditorAgent) ensureSeminalReferences(ctx context.Context, title, content string, report *BibliographicAuditReport, logf func(string)) string {
	if logf == nil {
		logf = func(string) {}
	}

	// 1. Check if the article already contains verified DOI links or active institutional references in its content
	verifiedLinks := reVerifiedDOILink.FindAllString(content, -1)
	if len(verifiedLinks) > 0 {
		return content
	}

	// Also check if any table row or citation actually present in the content has a valid DOI or recognized institution
	remainingCitations := citations.ParseCitations(content)
	for _, c := range remainingCitations {
		if c.DOI != "" || citations.IsInstitutionalCitation(c.RawText) {
			return content
		}
	}

	logf("⚠️ Aucune référence scientifique valide n'est présente dans l'article. Recherche et injection de publications fondatrices réelles...")

	// 2. Discover candidates using LLM and/or searcher
	candidates := b.discoverSeminalCandidates(ctx, title, content, logf)

	// 3. Authenticate candidates against Crossref / PubMed
	var verifiedList []citations.VerifiedCandidate
	for _, cand := range candidates {
		if b.citationValidator != nil {
			pc := citations.ParsedCitation{
				Authors: cand.Authors,
				Year:    cand.Year,
				Title:   cand.Title,
				Journal: cand.Journal,
				DOI:     cand.DOI,
				RawText: fmt.Sprintf("%s (%d) %s %s", strings.Join(cand.Authors, ", "), cand.Year, cand.Title, cand.Journal),
			}
			vc, err := b.citationValidator.VerifyCandidatePublication(ctx, pc)
			if err == nil && vc != nil && vc.DOI != "" {
				// Avoid duplicate DOIs
				dup := false
				for _, v := range verifiedList {
					if strings.EqualFold(v.DOI, vc.DOI) {
						dup = true
						break
					}
				}
				if !dup {
					verifiedList = append(verifiedList, *vc)
					logf(fmt.Sprintf("  ✨ [AUTHENTIFIÉ CROSSREF] %s (%d, %s) → DOI: %s", vc.Title, vc.Year, vc.Journal, vc.DOI))
				}
			} else {
				logf(fmt.Sprintf("  ⚠️ [NON VALIDÉ] %s : %v", cand.Title, err))
			}
		} else if cand.DOI != "" {
			// In isolated unit tests without validator, trust candidate if DOI provided
			verifiedList = append(verifiedList, citations.VerifiedCandidate{
				Authors: cand.Authors,
				Year:    cand.Year,
				Title:   cand.Title,
				Journal: cand.Journal,
				DOI:     cand.DOI,
			})
		}
	}

	// Fallback to direct Crossref search on title keywords if LLM candidate verification yielded no verified items
	if len(verifiedList) == 0 && b.citationValidator != nil && b.citationValidator.GetCrossrefClient() != nil {
		cleanTopic := CleanTopic(title)
		logf(fmt.Sprintf("  🔎 Recherche directe de publications fondatrices sur Crossref pour : %q...", cleanTopic))
		items, errCR := b.citationValidator.GetCrossrefClient().QueryBibliographic(ctx, citations.ParsedCitation{
			RawText: cleanTopic,
		})
		if errCR == nil && len(items) > 0 {
			for _, item := range items {
				itemDOI := citations.CleanDOIPrefix(item.DOI)
				if itemDOI == "" || !b.citationValidator.CheckDOIExists(ctx, itemDOI) {
					continue
				}
				actualAuthors := citations.ExtractItemAuthors(&item)
				candTitle := ""
				if len(item.Title) > 0 {
					candTitle = item.Title[0]
				}
				candJournal := ""
				if len(item.ContainerTitle) > 0 {
					candJournal = item.ContainerTitle[0]
				}
				candYear := 0
				if len(item.Issued.DateParts) > 0 && len(item.Issued.DateParts[0]) > 0 {
					candYear = item.Issued.DateParts[0][0]
				}
				if candTitle != "" && len(actualAuthors) > 0 {
					dup := false
					for _, v := range verifiedList {
						if strings.EqualFold(v.DOI, itemDOI) {
							dup = true
							break
						}
					}
					if !dup {
						verifiedList = append(verifiedList, citations.VerifiedCandidate{
							Authors: actualAuthors,
							Year:    candYear,
							Title:   candTitle,
							Journal: candJournal,
							DOI:     itemDOI,
						})
						logf(fmt.Sprintf("  ✨ [AUTHENTIFIÉ CROSSREF DIRECT] %s (%d, %s) → DOI: %s", candTitle, candYear, candJournal, itemDOI))
						if len(verifiedList) >= 3 {
							break
						}
					}
				}
			}
		}
	}

	if len(verifiedList) == 0 {
		logf("  ⚠️ Impossible d'authentifier les publications candidates dans les registres académiques.")
		return content
	}

	// 4. Generate formatted HTML table
	tableHTML := generateSeminalReferencesTable(verifiedList)

	// 5. Inject table into the article
	updatedContent := injectReferencesTable(content, tableHTML)

	// 6. Update report
	if report != nil {
		report.ValidatedDOIs += len(verifiedList)
		report.InjectedDOIs += len(verifiedList)
		report.Corrected++
		for _, v := range verifiedList {
			actionMsg := fmt.Sprintf("[RÉFÉRENCE FONDATRICE INJECTÉE] %s (%d, %s) → DOI: %s", v.Title, v.Year, v.Journal, v.DOI)
			report.Actions = append(report.Actions, actionMsg)
		}
	}

	logf(fmt.Sprintf("✅ %d référence(s) scientifique(s) fondatrice(s) authentifiée(s) et injectée(s) dans l'article.", len(verifiedList)))
	return updatedContent
}

func (b *BibliographicAuditorAgent) discoverSeminalCandidates(ctx context.Context, title, content string, logf func(string)) []seminalCandidateJSON {
	if b.provider == nil {
		return nil
	}

	sysPrompt := `Tu es un bibliothécaire scientifique et documentaliste de recherche (CNRS / Universités).
Ton rôle est d'identifier les publications scientifiques majeures et fondatrices réelles pour un article technique.`

	userPrompt := fmt.Sprintf(`L'article scientifique/technique suivant ne possède AUCUNE référence bibliographique valide :
TITRE DE L'ARTICLE : "%s"

EXTRAIT DU CONTENU :
"""
%s
"""

TA MISSION :
Identifie 2 ou 3 publications scientifiques FONDATRICES RÉELLES (articles fondateurs, revues majeures, études pionnières incontournables) sur le sujet traité (ex: Nature, Science, Cell, PNAS, Advanced Materials, Nature Materials, IEEE, etc.).

RÈGLES IMPÉRATIVES :
1. Chaque publication DOIT RÉELLEMENT EXISTER dans les registres académiques Crossref / PubMed.
2. Pas d'auteurs fictifs ni de titres imaginés. Indique les vrais noms de famille des auteurs (ex: ["Dickey", "Whitesides"] ou ["Toohey", "White"]), l'année réelle de publication, le vrai titre en anglais, la revue.
3. Indique le DOI officiel exact si tu le connais.

Réponds STRICTEMENT avec ce format JSON :
{
  "publications": [
    {
      "authors": ["Auteur1", "Auteur2"],
      "year": 2008,
      "title": "Exact English title",
      "journal": "Nature Materials",
      "doi": "10.1038/..."
    }
  ]
}`, title, fcTruncate(htmlToPlainText(content), 1200))

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: sysPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	res, err := llm.GenerateWithContinuation(ctx, b.provider, msgs, 2048, nil)
	if err != nil {
		logf(fmt.Sprintf("⚠️ Erreur lors de la découverte de références fondatrices : %v", err))
		return nil
	}

	cleanJSON := fcExtractJSON(res, '{', '}')
	var parsed seminalCandidateListJSON
	if errJSON := json.Unmarshal([]byte(cleanJSON), &parsed); errJSON != nil {
		logf(fmt.Sprintf("⚠️ JSON découverte non parsable : %v", errJSON))
		return nil
	}

	return parsed.Publications
}

func generateSeminalReferencesTable(verified []citations.VerifiedCandidate) string {
	var sb strings.Builder
	sb.WriteString(`<div class="table-responsive">` + "\n")
	sb.WriteString(`  <table class="table table-striped table-bordered my-4">` + "\n")
	sb.WriteString(`    <thead>` + "\n")
	sb.WriteString(`      <tr>` + "\n")
	sb.WriteString(`        <th>Auteurs</th>` + "\n")
	sb.WriteString(`        <th>Année</th>` + "\n")
	sb.WriteString(`        <th>Titre de la publication</th>` + "\n")
	sb.WriteString(`        <th>Revue</th>` + "\n")
	sb.WriteString(`        <th>DOI / Référence officielle</th>` + "\n")
	sb.WriteString(`      </tr>` + "\n")
	sb.WriteString(`    </thead>` + "\n")
	sb.WriteString(`    <tbody>` + "\n")

	for _, v := range verified {
		authorStr := formatCandidateAuthors(v.Authors)
		titleStr := html.EscapeString(v.Title)
		journalStr := html.EscapeString(v.Journal)
		cleanDOI := citations.CleanDOIPrefix(v.DOI)

		sb.WriteString(`      <tr>` + "\n")
		sb.WriteString(fmt.Sprintf(`        <td>%s</td>`+"\n", authorStr))
		sb.WriteString(fmt.Sprintf(`        <td>%d</td>`+"\n", v.Year))
		sb.WriteString(fmt.Sprintf(`        <td>%s</td>`+"\n", titleStr))
		sb.WriteString(fmt.Sprintf(`        <td><em>%s</em></td>`+"\n", journalStr))
		sb.WriteString(fmt.Sprintf(`        <td><a href="https://doi.org/%s" target="_blank" rel="noopener">DOI: %s</a></td>`+"\n", cleanDOI, cleanDOI))
		sb.WriteString(`      </tr>` + "\n")
	}

	sb.WriteString(`    </tbody>` + "\n")
	sb.WriteString(`  </table>` + "\n")
	sb.WriteString(`</div>`)

	return sb.String()
}

func formatCandidateAuthors(authors []string) string {
	if len(authors) == 0 {
		return "Auteurs de référence"
	}
	var clean []string
	for _, a := range authors {
		a = strings.TrimSpace(a)
		if a != "" {
			clean = append(clean, a)
		}
	}
	if len(clean) == 0 {
		return "Auteurs de référence"
	}
	if len(clean) > 3 {
		return fmt.Sprintf("%s, %s, %s et al.", clean[0], clean[1], clean[2])
	}
	return strings.Join(clean, ", ")
}

func injectReferencesTable(content, tableHTML string) string {
	loc := reRefHeading.FindStringIndex(content)
	if len(loc) == 2 {
		headingEnd := loc[1]
		rest := content[headingEnd:]
		articleCloseIdx := strings.Index(strings.ToLower(rest), "</article>")
		if articleCloseIdx != -1 {
			before := content[:headingEnd]
			after := rest[articleCloseIdx:]
			return before + "\n" + tableHTML + "\n" + after
		} else {
			return content[:headingEnd] + "\n" + tableHTML
		}
	}

	// No references heading found: inject before </article>
	articleCloseIdx := strings.LastIndex(strings.ToLower(content), "</article>")
	if articleCloseIdx != -1 {
		before := content[:articleCloseIdx]
		after := content[articleCloseIdx:]
		return before + "\n<h3>Références et Sources Documentaires</h3>\n" + tableHTML + "\n" + after
	}

	return content + "\n<h3>Références et Sources Documentaires</h3>\n" + tableHTML
}
