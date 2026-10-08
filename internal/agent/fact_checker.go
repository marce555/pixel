package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/scheduler"
)

// FactSearcher is the set of web tools the fact-checker uses to gather its OWN evidence.
// *WebAgent satisfies it.
type FactSearcher interface {
	SearchWeb(query string) (string, error)
	SearchWikipedia(query string) (string, error)
	ReadURLContent(targetURL string) (string, error)
}

// FactCheckerAgent is an independent verification cycle for Pixel's articles.
//
// Independence guarantees:
//   - it prefers a different model than the writer (cloud model, while Pixel writes locally);
//   - it only receives the final article, never Pixel's research context or reasoning;
//   - it only trusts evidence it retrieves itself from the web.
//
// Cycle: 1) claim extraction → 2) investigation (web) → 3) verdict per claim → 4) targeted corrections.
type FactCheckerAgent struct {
	provider    llm.Provider // writer's provider (adaptive), used to resolve the cloud model and as fallback
	searcher    FactSearcher
	maxClaims   int
	maxRefs     int
	rejectRatio float64
	forceLocal  bool

	// overridable for tests
	primaryOverride llm.Provider
	labelOverride   string
	concurrency     int
}

// NewFactCheckerAgent builds the fact-checker. Configuration (env):
//   - PIXEL_FACTCHECK_MODEL=cloud|local (default cloud, for independence from the local writer model)
//   - PIXEL_FACTCHECK_REJECT_RATIO (default 0.4): share of false claims above which the article is rejected
//   - PIXEL_FACTCHECK_MAX_CLAIMS (default 20)
func NewFactCheckerAgent(provider llm.Provider, searcher FactSearcher) *FactCheckerAgent {
	f := &FactCheckerAgent{
		provider:    provider,
		searcher:    searcher,
		maxClaims:   25,
		maxRefs:     8,
		rejectRatio: 0.4,
		forceLocal:  strings.EqualFold(strings.TrimSpace(os.Getenv("PIXEL_FACTCHECK_MODEL")), "local"),
	}
	if v, err := strconv.ParseFloat(os.Getenv("PIXEL_FACTCHECK_REJECT_RATIO"), 64); err == nil && v > 0 && v <= 1 {
		f.rejectRatio = v
	}
	if v, err := strconv.Atoi(os.Getenv("PIXEL_FACTCHECK_MAX_CLAIMS")); err == nil && v > 0 {
		f.maxClaims = v
	}
	return f
}

// ---------- model selection ----------

type fcModel struct {
	primary  llm.Provider
	fallback llm.Provider
	label    string
	mu       sync.Mutex
	degraded bool
}

func (f *FactCheckerAgent) resolveModel() *fcModel {
	if f.primaryOverride != nil {
		label := f.labelOverride
		if label == "" {
			label = "modèle de test"
		}
		return &fcModel{primary: f.primaryOverride, label: label}
	}
	m := &fcModel{primary: f.provider, label: "modèle local de Pixel (indépendance réduite)"}
	if f.forceLocal {
		return m
	}

	var apiKey string
	var baseURL string
	var modelName string

	type cloudGetter interface{ GetCloudProvider() llm.Provider }
	if g, ok := f.provider.(cloudGetter); ok {
		if cp, ok := g.GetCloudProvider().(*llm.OpenAICompatibleProvider); ok && cp != nil {
			apiKey = cp.APIKey
			baseURL = cp.BaseURL
			modelName = cp.ChatModel
		}
	}

	if apiKey == "" {
		if k := os.Getenv("GEMINI_API_KEY"); k != "" {
			apiKey = k
		} else if data, err := os.ReadFile(".key_Gemini.txt"); err == nil {
			apiKey = strings.TrimSpace(string(data))
		}
	}
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	}
	if modelName == "" || modelName == "gemini-2.5-flash" || modelName == "gemini-1.5-flash" {
		modelName = "gemini-3.8-flash"
	}

	if apiKey != "" {
		own := llm.NewOpenAICompatibleProvider(baseURL, apiKey, modelName, "")
		own.SetTemperature(0.1)
		m.primary = own
		m.fallback = f.provider
		m.label = modelName
	}
	return m
}

func (m *fcModel) generate(ctx context.Context, system, user string, maxTokens int) (string, error) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: system},
		{Role: llm.RoleUser, Content: user},
	}
	m.mu.Lock()
	useFallback := m.degraded && m.fallback != nil
	m.mu.Unlock()
	if !useFallback {
		res, err := llm.GenerateWithTokens(ctx, m.primary, msgs, maxTokens)
		if err == nil || m.fallback == nil {
			return res, err
		}
		m.mu.Lock()
		m.degraded = true
		m.mu.Unlock()
		fmt.Printf("[FactChecker] Modèle %s indisponible (%v), repli sur le modèle local.\n", m.label, err)
	}
	return llm.GenerateWithTokens(ctx, m.fallback, msgs, maxTokens)
}

func (m *fcModel) effectiveLabel() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.degraded {
		return m.label + " → repli local"
	}
	return m.label
}

// ---------- prompts ----------

const factCheckerSystem = `Tu es un vérificateur de faits (fact-checker) professionnel, indépendant et sceptique.
Tu n'as PAS écrit l'article que tu audites et tu ne fais pas confiance à son auteur : la charge de la preuve repose sur l'article.
Tu juges uniquement sur la base de preuves concrètes. Tu réponds STRICTEMENT dans le format JSON demandé, sans aucun texte autour.`

const claimExtractionPrompt = `Voici un article à auditer.
TITRE : "%s"

TEXTE (balises HTML retirées) :
"""
%s
"""

Extrais au maximum %d AFFIRMATIONS FACTUELLES VÉRIFIABLES (les plus déterminantes pour la véracité et la rigueur scientifique de l'article) :
- causalités physiques, climatiques ou géophysiques majeures (mécanismes d'impact, dynamique des systèmes) ;
- terminologie spécialisée et vocabulaire technique (attention particulière aux calques ou mauvaises traductions de l'anglais, néologismes) ;
- chiffres, statistiques, dates, unités, ordres de grandeur ;
- noms propres : organismes, projets, produits, études, publications, lois, normes, acronymes ;
- attributions (« X a démontré / publié Y ») et citations.
Ignore : opinions subjectives, transitions rhétoriques pures, et la section tableau de sources (traitée à part).

Pour chaque affirmation, sois TRÈS CONCIS :
- "claim" : l'affirmation précise (1 à 2 phrases max) ;
- "excerpt" : court extrait (8 à 20 mots) copié fidèlement du texte ;
- "kind" : "chiffre" | "date" | "nom" | "attribution" | "technique" | "scientifique" ;
- "query" : 2 à 4 mots-clés de recherche web essentiels ;
- "central" : true si l'affirmation porte la thèse principale, sinon false.

Réponds UNIQUEMENT avec un tableau JSON valide et concis :
[{"claim":"...","excerpt":"...","kind":"...","query":"...","central":false}]`

const claimVerdictPrompt = `AFFIRMATION À VÉRIFIER (tirée d'un article) :
"%s"
Contexte dans l'article : "%s"

PREUVES QUE TU AS COLLECTÉES TOI-MÊME SUR LE WEB :
"""
%s
"""

Rends un verdict parmi :
- "confirmé" : les preuves (ou un fait notoire, consensuel et incontesté) soutiennent l'affirmation telle qu'écrite ;
- "imprécis" : globalement juste mais un détail est inexact, exagéré, mal attribué, daté, trompeur, ou utilise une terminologie impropre (calque de l'anglais comme « broutage de sel » pour « rejet de saumure », mot forgé comme « mergelé » pour « banquise », confusion entre plongée convective et remontée d'eau / upwelling, ou traduction approximative d'université comme « Université du Japon à Kyoto » au lieu de « Université de Kyoto ») ;
- "faux" : les preuves contredisent l'affirmation, OU affirmation physique/scientifique extraordinaire sans aucun fondement dans la littérature scientifique (hallucination de l'IA, ex: déplacement du pôle Nord géographique par arrêt océanique, dérive des plaques tectoniques induite par la circulation thermohaline), OU contradiction biologique majeure (ex: affirmer que le base editing ou le prime editing nécessitent des cassures double-brin / DSB, alors qu'ils utilisent une nCas9/dCas9 précisément pour NE PAS induire de DSB), OU entité d'entreprise fictive / chimérique (ex: « ExaCell Therapeutics » est une hallucination issue du médicament « exa-cel » / Casgevy dont les véritables promoteurs sont Vertex Pharmaceuticals et CRISPR Therapeutics) ;
- "non vérifiable" : affirmation d'ordre anecdotique où les preuves sont muettes. Si l'affirmation prétend décrire un fait scientifique, une entreprise biotechnologique ou une causalité technique sans aucun consensus, exige son retrait ou sa rectification.

Si le verdict est "imprécis" ou "faux", OU s'il est "non vérifiable" portant sur une causalité scientifique, une entité biotech ou une affiliation technique :
rédige dans "correction" une formulation de remplacement en français, rigoureuse et sobre, qui :
- rectifie selon le consensus scientifique établi et les promoteurs réels (ex: remplacer « ExaCell Therapeutics » par « Vertex Pharmaceuticals et CRISPR Therapeutics (pour la thérapie Casgevy / exa-cel) », rectifier « Université du Japon à Kyoto » en « Université de Kyoto », rétablir que le base/prime editing n'induit AUCUNE cassure double-brin) ;
- OU, si l'affirmation est une hallucination pseudo-scientifique (ex: mouvement des pôles ou des plaques tectoniques induit par les courants océaniques), demande expressément la SUPPRESSION du passage en indiquant "[SUPPRIMER]" ou en reformulant sur les réelles conséquences documentées (effets climatiques et écosystémiques) ;
- utilise le vocabulaire scientifique français exact (ex: « rejet de saumure », « banquise », « remontée d'eau / upwelling ») ;
- n'introduit AUCUN fait nouveau qui ne figure pas dans les preuves.
Sinon laisse "correction" vide.

Réponds UNIQUEMENT avec ce JSON :
{"verdict":"...","evidence":"citation ou résumé court de la preuve décisive (1-2 phrases)","source_url":"URL de la preuve ou vide","correction":"..."}`

const referenceVerdictPrompt = `Un article cite la référence documentaire suivante dans son tableau de sources :
"%s"

RÉSULTATS DE RECHERCHE QUE TU AS COLLECTÉS :
"""
%s
"""

Cette référence existe-t-elle réellement et correspond-elle à la description donnée ?
- "confirmé" : elle existe et la description est fidèle ;
- "imprécis" : elle existe mais la description/le type/l'auteur est inexact ;
- "faux" : elle n'existe pas ou ne correspond manifestement pas (référence inventée ou placeholder IT/RFC inapproprié) ;
- "non vérifiable" : les résultats ne permettent pas de conclure.

Réponds UNIQUEMENT avec ce JSON :
{"verdict":"...","evidence":"justification courte","source_url":"URL officielle trouvée ou vide"}`

const blockCorrectionPrompt = `Voici un bloc HTML extrait d'un article :
%s

Une vérification factuelle a établi que l'affirmation suivante est %s :
"%s"
Formulation corrigée validée par les sources :
"%s"

Réécris CE BLOC UNIQUEMENT en intégrant la correction :
- si la correction contient "[SUPPRIMER]" ou demande d'éliminer une affirmation hallucinée sans fondement, supprime la phrase ou l'allégation fautive tout en préservant la fluidité grammaticale du reste du bloc (si tout le paragraphe était halluciné, réponds simplement avec une balise vide ou une synthèse sobre des impacts réels) ;
- conserve exactement la même balise englobante, la structure HTML, le style et le reste du texte ;
- ne modifie que ce qui est nécessaire pour rendre le passage exact et scientifiquement rigoureux ;
- n'ajoute AUCUN fait nouveau.
Réponds UNIQUEMENT avec le bloc HTML corrigé, sans commentaire ni bloc de code Markdown.`

// ---------- data ----------

type extractedClaim struct {
	Claim   string `json:"claim"`
	Excerpt string `json:"excerpt"`
	Kind    string `json:"kind"`
	Query   string `json:"query"`
	Central bool   `json:"central"`
}

type verdictJSON struct {
	Verdict    string `json:"verdict"`
	Evidence   string `json:"evidence"`
	SourceURL  string `json:"source_url"`
	Correction string `json:"correction"`
}

type referenceRow struct {
	rowHTML string
	text    string
}

// ---------- main cycle ----------

// Check runs the full independent verification cycle and returns the report and the corrected HTML.
// Implements scheduler.ArticleFactChecker.
func (f *FactCheckerAgent) Check(ctx context.Context, title, content string, logf func(string)) (*scheduler.FactCheckReport, string, error) {
	if logf == nil {
		logf = func(string) {}
	}
	if strings.TrimSpace(content) == "" {
		return nil, content, fmt.Errorf("article vide")
	}
	if f.provider == nil && f.primaryOverride == nil {
		return nil, content, fmt.Errorf("aucun modèle disponible pour la vérification")
	}

	model := f.resolveModel()
	report := &scheduler.FactCheckReport{Model: model.label, CheckedAt: time.Now()}
	logf(fmt.Sprintf("🔎 Vérificateur indépendant démarré (modèle : %s).", model.label))

	ctx, cancel := context.WithTimeout(ctx, 25*time.Minute)
	defer cancel()

	// 1. Extraction
	plain := htmlToPlainText(content)
	claims, err := f.extractClaims(ctx, model, title, plain)
	if err != nil {
		report.Verdict = scheduler.FactCheckSkipped
		report.Summary = fmt.Sprintf("Extraction des affirmations impossible : %v", err)
		report.Warnings = append(report.Warnings, report.Summary)
		return report, content, nil
	}
	refs := extractReferenceRows(content)
	if len(refs) > f.maxRefs {
		refs = refs[:f.maxRefs]
	}
	logf(fmt.Sprintf("Étape 1/4 : %d affirmation(s) et %d référence(s) à vérifier.", len(claims), len(refs)))

	// 2 & 3. Investigation + verdicts (in parallel, bounded)
	verdicts := make([]scheduler.ClaimVerdict, len(claims)+len(refs))
	centralFlags := make([]bool, len(claims))
	var visitedMu sync.Mutex
	visited := map[string]bool{}
	addVisited := func(u string) {
		if u == "" {
			return
		}
		visitedMu.Lock()
		visited[u] = true
		visitedMu.Unlock()
	}

	conc := f.concurrency
	if conc <= 0 {
		conc = 3
		if model.fallback == nil { // local-only model: avoid saturating the NPU
			conc = 1
		}
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var doneMu sync.Mutex
	done := 0
	total := len(verdicts)
	progress := func(label string) {
		doneMu.Lock()
		done++
		n := done
		doneMu.Unlock()
		logf(fmt.Sprintf("Étape 2-3/4 : vérification %d/%d – %s", n, total, label))
	}

	for i, c := range claims {
		centralFlags[i] = c.Central
		wg.Add(1)
		go func(i int, c extractedClaim) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v := f.verifyClaim(ctx, model, c, addVisited)
			verdicts[i] = v
			progress(fmt.Sprintf("[%s] %s", v.Verdict, fcTruncate(c.Claim, 80)))
		}(i, c)
	}
	for j, r := range refs {
		wg.Add(1)
		go func(idx int, r referenceRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			v := f.verifyReference(ctx, model, r, addVisited)
			verdicts[idx] = v
			progress(fmt.Sprintf("[%s] référence : %s", v.Verdict, fcTruncate(r.text, 70)))
		}(len(claims)+j, r)
	}
	wg.Wait()

	if ctx.Err() != nil {
		report.Warnings = append(report.Warnings, "Délai de vérification dépassé : certaines affirmations n'ont pas pu être contrôlées.")
	}

	// 4. Corrections
	corrected := content
	centralUncorrected := false
	for i := range verdicts {
		v := &verdicts[i]
		if v.IsReference {
			if v.Verdict == scheduler.ClaimFalse {
				row := refs[i-len(claims)].rowHTML
				if strings.Contains(corrected, row) {
					corrected = strings.Replace(corrected, row, "", 1)
					v.Applied = true
					report.RemovedRefs++
				}
			}
			continue
		}
		if strings.TrimSpace(v.Correction) == "" || v.Verdict == scheduler.ClaimConfirmed {
			if v.Verdict == scheduler.ClaimFalse && centralFlags[i] {
				centralUncorrected = true
			}
			continue
		}
		newContent, ok := f.applyCorrection(ctx, model, corrected, *v)
		if ok {
			corrected = newContent
			v.Applied = true
			report.Corrections++
		} else {
			report.Warnings = append(report.Warnings, fmt.Sprintf("Correction non appliquée (passage introuvable) : « %s »", fcTruncate(v.Claim, 90)))
			if v.Verdict == scheduler.ClaimFalse && centralFlags[i] {
				centralUncorrected = true
			}
		}
	}
	corrected = removeEmptyTables(corrected)
	corrected = SanitizeDeepContent(corrected, title)
	logf(fmt.Sprintf("Étape 4/4 : %d correction(s) appliquée(s), %d référence(s) retirée(s).", report.Corrections, report.RemovedRefs))

	// Report
	report.Claims = verdicts
	nonRef := 0
	for _, v := range verdicts {
		switch v.Verdict {
		case scheduler.ClaimConfirmed:
			report.Confirmed++
		case scheduler.ClaimImprecise:
			report.Imprecise++
		case scheduler.ClaimFalse:
			report.False++
		default:
			report.Unverifiable++
		}
		if !v.IsReference {
			nonRef++
		}
	}
	for u := range visited {
		report.SourcesVisited = append(report.SourcesVisited, u)
	}

	falseClaims := 0
	for _, v := range verdicts {
		if !v.IsReference && v.Verdict == scheduler.ClaimFalse {
			falseClaims++
		}
	}
	ratio := 0.0
	if nonRef > 0 {
		ratio = float64(falseClaims) / float64(nonRef)
	}

	switch {
	case len(verdicts) == 0:
		report.Verdict = scheduler.FactCheckSkipped
		report.Summary = "Aucune affirmation vérifiable n'a pu être extraite."
	case (falseClaims > 0 && ratio >= f.rejectRatio) || centralUncorrected:
		report.Verdict = scheduler.FactCheckRejected
		if centralUncorrected {
			report.Summary = "La thèse centrale de l'article est contredite par les sources et n'a pas pu être corrigée."
		} else {
			report.Summary = fmt.Sprintf("%d affirmation(s) fausse(s) sur %d (%.0f%% ≥ seuil %.0f%%) : l'article est trop inexact pour être publié.", falseClaims, nonRef, ratio*100, f.rejectRatio*100)
		}
	case report.WasAdjusted():
		report.Verdict = scheduler.FactCheckCorrected
		report.Summary = fmt.Sprintf("%d correction(s) et %d référence(s) retirée(s) sur %d élément(s) vérifié(s).", report.Corrections, report.RemovedRefs, len(verdicts))
	default:
		report.Verdict = scheduler.FactCheckValidated
		report.Summary = fmt.Sprintf("%d élément(s) vérifié(s), aucun ajustement nécessaire.", len(verdicts))
	}
	report.Model = model.effectiveLabel()
	if strings.Contains(report.Model, "repli local") || model.fallback == nil && f.primaryOverride == nil {
		report.Warnings = append(report.Warnings, "Vérification effectuée avec le modèle local de Pixel : indépendance réduite.")
	}
	logf(fmt.Sprintf("🔎 Verdict du vérificateur : %s – %s", report.Verdict, report.Summary))

	return report, corrected, nil
}

// ---------- step 1 ----------

func (f *FactCheckerAgent) extractClaims(ctx context.Context, model *fcModel, title, plain string) ([]extractedClaim, error) {
	text := plain
	if len([]rune(text)) > 24000 {
		text = string([]rune(text)[:24000])
	}
	res, err := model.generate(ctx, factCheckerSystem, fmt.Sprintf(claimExtractionPrompt, title, text, f.maxClaims), 8192)
	if err != nil {
		return nil, err
	}
	var claims []extractedClaim
	if err := json.Unmarshal([]byte(fcExtractJSON(res, '[', ']')), &claims); err != nil {
		return nil, fmt.Errorf("réponse JSON invalide : %w", err)
	}
	var out []extractedClaim
	for _, c := range claims {
		if strings.TrimSpace(c.Claim) == "" {
			continue
		}
		if c.Query == "" {
			c.Query = c.Claim
		}
		out = append(out, c)
		if len(out) >= f.maxClaims {
			break
		}
	}
	return out, nil
}

// ---------- step 2-3 ----------

func (f *FactCheckerAgent) gatherEvidence(query string, wiki bool, addVisited func(string)) (string, string) {
	if f.searcher == nil {
		return "", ""
	}
	var b strings.Builder
	var firstURL string
	if res, err := f.searcher.SearchWeb(query); err == nil && strings.TrimSpace(res) != "" {
		b.WriteString("[RECHERCHE WEB]\n" + fcTruncate(res, 3000) + "\n")
		urls := extractResultURLs(res)
		read := 0
		for _, u := range urls {
			if read >= 1 {
				break
			}
			page, err := f.searcher.ReadURLContent(u)
			if err != nil || len(strings.TrimSpace(page)) < 200 {
				continue
			}
			addVisited(u)
			if firstURL == "" {
				firstURL = u
			}
			b.WriteString(fmt.Sprintf("\n[CONTENU DE %s]\n%s\n", u, fcTruncate(page, 5000)))
			read++
		}
	}
	if wiki {
		if res, err := f.searcher.SearchWikipedia(query); err == nil && strings.TrimSpace(res) != "" {
			b.WriteString("\n[WIKIPÉDIA]\n" + fcTruncate(res, 2500) + "\n")
			addVisited("https://fr.wikipedia.org/ (" + query + ")")
		}
	}
	return b.String(), firstURL
}

func (f *FactCheckerAgent) verifyClaim(ctx context.Context, model *fcModel, c extractedClaim, addVisited func(string)) scheduler.ClaimVerdict {
	v := scheduler.ClaimVerdict{Claim: c.Claim, Excerpt: c.Excerpt, Kind: c.Kind, Verdict: scheduler.ClaimUnverifiable}
	if ctx.Err() != nil {
		v.Evidence = "Non vérifiée (délai dépassé)."
		return v
	}
	evidence, firstURL := f.gatherEvidence(c.Query, true, addVisited)
	if strings.TrimSpace(evidence) == "" {
		evidence = "(Aucun résultat de recherche n'a pu être obtenu.)"
	}
	res, err := model.generate(ctx, factCheckerSystem, fmt.Sprintf(claimVerdictPrompt, c.Claim, c.Excerpt, evidence), 1024)
	if err != nil {
		v.Evidence = fmt.Sprintf("Erreur du modèle : %v", err)
		return v
	}
	var vj verdictJSON
	if err := json.Unmarshal([]byte(fcExtractJSON(res, '{', '}')), &vj); err != nil {
		v.Evidence = "Réponse du vérificateur illisible."
		return v
	}
	v.Verdict = normalizeVerdict(vj.Verdict)
	v.Evidence = strings.TrimSpace(vj.Evidence)
	v.SourceURL = strings.TrimSpace(vj.SourceURL)
	if v.SourceURL == "" && v.Verdict != scheduler.ClaimUnverifiable {
		v.SourceURL = firstURL
	}
	if v.Verdict != scheduler.ClaimConfirmed {
		v.Correction = strings.TrimSpace(vj.Correction)
	}
	return v
}

func (f *FactCheckerAgent) verifyReference(ctx context.Context, model *fcModel, r referenceRow, addVisited func(string)) scheduler.ClaimVerdict {
	v := scheduler.ClaimVerdict{Claim: r.text, Kind: "référence", Verdict: scheduler.ClaimUnverifiable, IsReference: true}
	if ctx.Err() != nil || f.searcher == nil {
		return v
	}
	// Explicit URL in the row: check it actually resolves.
	if u := firstHTTPURL(r.rowHTML + " " + r.text); u != "" {
		if page, err := f.searcher.ReadURLContent(u); err == nil && len(strings.TrimSpace(page)) > 100 {
			addVisited(u)
		}
	}
	query := fcTruncate(r.text, 160)
	res, err := f.searcher.SearchWeb(query)
	if err != nil || strings.TrimSpace(res) == "" {
		// No search results: never remove a reference on the basis of a failed search.
		v.Evidence = "Recherche indisponible : référence conservée."
		return v
	}
	out, err := model.generate(ctx, factCheckerSystem, fmt.Sprintf(referenceVerdictPrompt, r.text, fcTruncate(res, 3500)), 512)
	if err != nil {
		return v
	}
	var vj verdictJSON
	if err := json.Unmarshal([]byte(fcExtractJSON(out, '{', '}')), &vj); err != nil {
		return v
	}
	v.Verdict = normalizeVerdict(vj.Verdict)
	v.Evidence = strings.TrimSpace(vj.Evidence)
	v.SourceURL = strings.TrimSpace(vj.SourceURL)
	return v
}

// ---------- step 4 ----------

var blockTags = []string{"p", "li", "td", "h2", "h3", "h4", "blockquote", "figcaption", "dd"}

var blockRegexps = func() []*regexp.Regexp {
	var res []*regexp.Regexp
	for _, t := range blockTags {
		res = append(res, regexp.MustCompile(`(?is)<`+t+`\b[^>]*>.*?</`+t+`>`))
	}
	return res
}()

// findBlock locates the smallest HTML block whose visible text contains (or best overlaps) the excerpt.
func findBlock(content, excerpt string) string {
	target := normalizeText(excerpt)
	if target == "" {
		return ""
	}
	best, bestScore := "", 0.0
	for _, re := range blockRegexps {
		for _, blk := range re.FindAllString(content, -1) {
			txt := normalizeText(htmlToPlainText(blk))
			if txt == "" {
				continue
			}
			if strings.Contains(txt, target) {
				if best == "" || bestScore < 1 || len(blk) < len(best) {
					best, bestScore = blk, 1
				}
				continue
			}
			if bestScore >= 1 {
				continue
			}
			if s := wordOverlap(target, txt); s > bestScore {
				best, bestScore = blk, s
			}
		}
	}
	if bestScore >= 0.7 {
		return best
	}
	return ""
}

func (f *FactCheckerAgent) applyCorrection(ctx context.Context, model *fcModel, content string, v scheduler.ClaimVerdict) (string, bool) {
	excerpt := v.Excerpt
	if excerpt == "" {
		excerpt = v.Claim
	}
	block := findBlock(content, excerpt)
	if block == "" {
		return content, false
	}
	res, err := model.generate(ctx, factCheckerSystem+"\nPour cette tâche, tu réponds avec du HTML et non du JSON.",
		fmt.Sprintf(blockCorrectionPrompt, block, v.Verdict, v.Claim, v.Correction), 2048)
	if err != nil {
		return content, false
	}
	newBlock := strings.TrimSpace(cleanMarkdownWraps(res))
	if newBlock == "" || newBlock == "<p></p>" || strings.EqualFold(newBlock, "[SUPPRIMER]") {
		return strings.Replace(content, block, "", 1), true
	}
	if !strings.HasPrefix(newBlock, "<") || len(newBlock) > len(block)*3+400 {
		return content, false
	}
	if newBlock == block {
		return content, false
	}
	return strings.Replace(content, block, newBlock, 1), true
}

// SanitizeDeepContent forwards to scheduler.SanitizeDeepContent.
func SanitizeDeepContent(content, title string) string {
	return scheduler.SanitizeDeepContent(content, title)
}

// ---------- reference table helpers ----------

var (
	reTable = regexp.MustCompile(`(?is)<table\b[^>]*>.*?</table>`)
	reTHead = regexp.MustCompile(`(?is)<thead\b[^>]*>.*?</thead>`)
	reTBody = regexp.MustCompile(`(?is)<tbody\b[^>]*>(.*?)</tbody>`)
	reTR    = regexp.MustCompile(`(?is)<tr\b[^>]*>.*?</tr>`)
	reTH    = regexp.MustCompile(`(?is)<th\b`)
)

func isReferenceTable(table string) bool {
	head := reTHead.FindString(table)
	if head == "" {
		if rows := reTR.FindAllString(table, 1); len(rows) > 0 {
			head = rows[0]
		}
	}
	h := strings.ToLower(htmlToPlainText(head))
	return strings.Contains(h, "référence") || strings.Contains(h, "reference") || strings.Contains(h, "source")
}

func extractReferenceRows(content string) []referenceRow {
	var rows []referenceRow
	for _, table := range reTable.FindAllString(content, -1) {
		if !isReferenceTable(table) {
			continue
		}
		body := table
		if m := reTBody.FindStringSubmatch(table); len(m) > 1 {
			body = m[1]
		}
		for _, tr := range reTR.FindAllString(body, -1) {
			if reTH.MatchString(tr) {
				continue
			}
			txt := strings.Join(strings.Fields(htmlToPlainText(strings.ReplaceAll(tr, "</td>", " — </td>"))), " ")
			txt = strings.TrimSuffix(strings.TrimSpace(txt), "—")
			if len(txt) < 5 {
				continue
			}
			rows = append(rows, referenceRow{rowHTML: tr, text: strings.TrimSpace(txt)})
		}
	}
	return rows
}

// removeEmptyTables drops tables whose body has no row left after reference removal.
func removeEmptyTables(content string) string {
	return reTable.ReplaceAllStringFunc(content, func(table string) string {
		if m := reTBody.FindStringSubmatch(table); len(m) > 1 && len(reTR.FindAllString(m[1], -1)) == 0 {
			return ""
		}
		return table
	})
}

// ---------- generic helpers ----------

var (
	reAnyTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlockClose = regexp.MustCompile(`(?i)</(p|li|h[1-6]|tr|td|th|div|pre|blockquote)>|<br\s*/?>`)
	reURL        = regexp.MustCompile(`https?://[^\s"'<>)\]]+`)
	reLienLine   = regexp.MustCompile(`(?m)Lien:\s*(\S+)`)
	reNonWord    = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

func htmlToPlainText(s string) string {
	s = reBlockClose.ReplaceAllString(s, "$0\n")
	s = reAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func normalizeText(s string) string {
	s = strings.ToLower(html.UnescapeString(s))
	s = reNonWord.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func wordOverlap(target, text string) float64 {
	tw := strings.Fields(target)
	if len(tw) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range strings.Fields(text) {
		set[w] = true
	}
	hit := 0
	for _, w := range tw {
		if set[w] {
			hit++
		}
	}
	return float64(hit) / float64(len(tw))
}

// extractResultURLs pulls result links from WebAgent.SearchWeb output, decoding DuckDuckGo redirects.
func extractResultURLs(res string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, m := range reLienLine.FindAllStringSubmatch(res, -1) {
		u := m[1]
		if strings.HasPrefix(u, "//") {
			u = "https:" + u
		}
		if parsed, err := url.Parse(u); err == nil {
			if real := parsed.Query().Get("uddg"); real != "" {
				u = real
			}
		}
		if !strings.HasPrefix(u, "http") || seen[u] {
			continue
		}
		seen[u] = true
		urls = append(urls, u)
	}
	return urls
}

func firstHTTPURL(s string) string {
	return strings.TrimRight(reURL.FindString(s), ".,;")
}

// extractJSON returns the substring between the first open and the last close delimiter,
// with graceful recovery if a JSON array was truncated before the closing bracket.
func fcExtractJSON(s string, open, close byte) string {
	s = cleanMarkdownWraps(s)
	i := strings.IndexByte(s, open)
	if i == -1 {
		return s
	}
	j := strings.LastIndexByte(s, close)
	if j != -1 && j > i {
		return s[i : j+1]
	}
	// Truncated array rescue: find last complete object brace and close the array
	if open == '[' && close == ']' {
		if lastBrace := strings.LastIndexByte(s[i:], '}'); lastBrace != -1 {
			return s[i:i+lastBrace+1] + "]"
		}
	}
	return s
}

func normalizeVerdict(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "confirm"):
		return scheduler.ClaimConfirmed
	case strings.HasPrefix(v, "impr"), strings.Contains(v, "partiel"):
		return scheduler.ClaimImprecise
	case strings.HasPrefix(v, "faux"), strings.HasPrefix(v, "false"), strings.Contains(v, "inexact"):
		return scheduler.ClaimFalse
	default:
		return scheduler.ClaimUnverifiable
	}
}

func fcTruncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
