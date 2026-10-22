package scheduler

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// ArticleFactChecker is an independent verification cycle that audits the factual accuracy
// of an article against external sources and returns a report plus the corrected content.
// It deliberately receives only the article (not Pixel's research context or reasoning).
type ArticleFactChecker interface {
	Check(ctx context.Context, title, content string, logf func(string)) (*FactCheckReport, string, error)
}

// BibliographicAuditReport summarizes the actions taken by the bibliographic auditor.
type BibliographicAuditReport struct {
	Conserved        int      `json:"conserved"`
	Corrected        int      `json:"corrected"`
	Replaced         int      `json:"replaced"`
	Removed          int      `json:"removed"`
	ValidatedDOIs    int      `json:"validated_dois,omitempty"`
	InjectedDOIs     int      `json:"injected_dois,omitempty"`
	ChimerasRejected int      `json:"chimeras_rejected,omitempty"`
	Institutional    int      `json:"institutional,omitempty"`
	Actions          []string `json:"actions"`
}

func (r *BibliographicAuditReport) TotalAdjustments() int {
	if r == nil {
		return 0
	}
	return r.Corrected + r.Replaced + r.Removed + r.InjectedDOIs
}

// mergeBiblioReports combines a previous audit report (from an earlier pass on the same draft)
// with the report of a new pass, so that earlier actions are not lost on retries.
func mergeBiblioReports(prev, next *BibliographicAuditReport) *BibliographicAuditReport {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	merged := *prev
	merged.Conserved = next.Conserved // état courant de l'article
	merged.Corrected += next.Corrected
	merged.Replaced += next.Replaced
	merged.Removed += next.Removed
	merged.ValidatedDOIs += next.ValidatedDOIs
	merged.InjectedDOIs += next.InjectedDOIs
	merged.ChimerasRejected += next.ChimerasRejected
	merged.Institutional += next.Institutional
	merged.Actions = append(append([]string{}, prev.Actions...), next.Actions...)
	return &merged
}

// ArticleBibliographicAuditor conducts an explicit, specialized audit of bibliographic references,
// author co-signatures (detecting AI chimeras), discovery dates, and laboratory/university affiliations.
type ArticleBibliographicAuditor interface {
	Audit(ctx context.Context, title, content string, logf func(string)) (string, *BibliographicAuditReport, error)
}

// FactCheckEnabled reports whether the independent fact-checking cycle is enabled (PIXEL_FACTCHECK, default on).
func FactCheckEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PIXEL_FACTCHECK")))
	return v != "off" && v != "false" && v != "0"
}

// FactCheckRequired reports whether a fact-check that could not be completed must block publication (PIXEL_FACTCHECK_REQUIRED, default on).
func FactCheckRequired() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PIXEL_FACTCHECK_REQUIRED")))
	return v != "off" && v != "false" && v != "0"
}

// AppliYouAIEnhanceEnabled reports whether the AppliYou "Améliorer avec l'IA" button should be used.
// It rewrites the article after verification and may reintroduce inaccuracies, so it is disabled
// by default whenever the fact-checker is active (override with PIXEL_APPLIYOU_AI_ENHANCE=on).
func AppliYouAIEnhanceEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PIXEL_APPLIYOU_AI_ENHANCE")))
	switch v {
	case "on", "true", "1":
		return true
	case "off", "false", "0":
		return false
	}
	return !FactCheckEnabled()
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// FormatFactCheckSummary renders the fact-check report as a user-facing Markdown block,
// stating explicitly whether the independent verifier had to adjust Pixel's statements.
func FormatFactCheckSummary(r *FactCheckReport) string {
	if r == nil {
		return "\n🔎 **Vérification factuelle** : non effectuée (aucun agent vérificateur configuré).\n"
	}
	var b strings.Builder
	model := r.Model
	if model == "" {
		model = "modèle inconnu"
	}
	b.WriteString(fmt.Sprintf("\n🔎 **Vérification factuelle** (agent indépendant – %s) : **%s**\n", model, r.Verdict))

	total := len(r.Claims)
	switch {
	case r.Verdict == FactCheckSkipped:
		b.WriteString("⚠️ La vérification n'a pas pu être menée à terme : l'article n'a **pas** été validé factuellement.\n")
	case r.WasAdjusted():
		b.WriteString(fmt.Sprintf("✏️ L'agent vérificateur **a dû ajuster mes propos** : %d correction(s) sur %d affirmation(s) vérifiée(s)", r.Corrections, total))
		if r.RemovedRefs > 0 {
			b.WriteString(fmt.Sprintf(", %d référence(s) invérifiable(s) retirée(s)", r.RemovedRefs))
		}
		b.WriteString(".\n")
		shown := 0
		for _, c := range r.Claims {
			if !c.Applied {
				continue
			}
			if shown >= 6 {
				b.WriteString("  • …\n")
				break
			}
			line := fmt.Sprintf("  • [%s] « %s »", c.Verdict, truncateRunes(c.Claim, 110))
			if c.IsReference {
				line += " → retirée"
			} else if c.Correction != "" {
				line += " → « " + truncateRunes(c.Correction, 110) + " »"
			}
			if c.SourceURL != "" {
				line += " (source : " + c.SourceURL + ")"
			}
			b.WriteString(line + "\n")
			shown++
		}
	default:
		b.WriteString(fmt.Sprintf("✅ L'agent vérificateur n'a eu **aucun ajustement** à apporter à mes propos (%d affirmation(s) vérifiée(s)).\n", total))
	}
	b.WriteString(fmt.Sprintf("📊 ✅ %d confirmée(s) · 〰️ %d imprécise(s) · ❔ %d non vérifiable(s) · ❌ %d fausse(s)\n", r.Confirmed, r.Imprecise, r.Unverifiable, r.False))
	for _, w := range r.Warnings {
		b.WriteString("⚠️ " + w + "\n")
	}
	return b.String()
}

// FormatBibliographicAuditSummary formats the report into Markdown for task reports.
func FormatBibliographicAuditSummary(r *BibliographicAuditReport) string {
	if r == nil || (r.Conserved == 0 && r.TotalAdjustments() == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n📚 **Audit Bibliographique & Affiliations Académiques** :\n")
	if r.ValidatedDOIs > 0 || r.InjectedDOIs > 0 {
		b.WriteString(fmt.Sprintf("🔬 *Validation API (Crossref & PubMed)* : %d authentifiée(s), %d lien(s) DOI officiel(s) injecté(s).\n", r.ValidatedDOIs, r.InjectedDOIs))
	}
	if r.TotalAdjustments() > 0 {
		b.WriteString(fmt.Sprintf("✏️ %d ajustement(s) critique(s) : %d corrigée(s), %d remplacée(s), %d retirée(s)", r.TotalAdjustments(), r.Corrected, r.Replaced, r.Removed))
		if r.Conserved > 0 {
			b.WriteString(fmt.Sprintf(", %d confirmée(s)", r.Conserved))
		}
		b.WriteString(".\n")
		for _, a := range r.Actions {
			b.WriteString("  • " + a + "\n")
		}
	} else {
		b.WriteString(fmt.Sprintf("✅ Toutes les références et affiliations ont été vérifiées et confirmées exactes (%d confirmée(s)).\n", r.Conserved))
	}
	return b.String()
}
