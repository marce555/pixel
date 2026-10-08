package scheduler

import (
	"strings"
	"testing"
)

func TestCleanToSemanticHTMLWithUserSample(t *testing.T) {
	rawMarkdown := `# L'Architecture Cognitive Prédictive : Quand Notre Cerveau Devient un Super-Ordinateur Imaginatif
Notre perception du monde n'est pas une simple fenêtre transparente sur la réalité objective. Elle est, au contraire, une construction active, complexe et constamment mise à jour par notre cerveau.

## 1. Les Fondations : Le Traitement Prédictif comme Paradigme Dominant
Pour comprendre la complexité du monitoring réel et de la reconstruction visuelle, il est impératif de revenir aux racines théoriques :
* **L'anticipation active** : Nous ne voyons pas seulement, nous anticipons.
* **La minimisation de la divergence** : L'objectif du système est de réduire la différence entre ce qui est prédit et ce qui est perçu.

## 2. Réalité Monitoring : La Sentinelle de l'Erreur Prédictive
Si le traitement prédictif est le moteur, alors la réalité monitoring en est le système d'alerte.
1. Validation contextuelle
2. Détection d'anomalies

$$P(H|I) \propto P(I|H) \times P(H)^{w}$$

Le tableau suivant illustre comment le traitement prédictif, la réalité monitoring et la mémoire épisodique interagissent. <`

	html := CleanToSemanticHTML(rawMarkdown, "L'Architecture Cognitive Prédictive")

	// 1. Must NOT contain raw markdown markers
	if strings.Contains(html, "# ") || strings.Contains(html, "## ") {
		t.Errorf("expected no raw markdown headers (# or ##), got:\n%s", html)
	}
	if strings.Contains(html, "**") {
		t.Errorf("expected no raw markdown bold (**), got:\n%s", html)
	}
	if strings.Contains(html, "$$") {
		t.Errorf("expected no raw latex ($$), got:\n%s", html)
	}

	// 2. Must NOT end with trailing '<'
	if strings.HasSuffix(strings.TrimSpace(html), "<") {
		t.Errorf("expected no trailing '<', got:\n%s", html)
	}

	// 3. Must contain semantic HTML tags
	if !strings.Contains(html, "<article") {
		t.Errorf("expected <article> tag")
	}
	if !strings.Contains(html, "<h2>") {
		t.Errorf("expected <h2> tag")
	}
	if !strings.Contains(html, "<strong>L'anticipation active</strong>") {
		t.Errorf("expected <strong> tag for bold text")
	}
	if !strings.Contains(html, "<ul>") || !strings.Contains(html, "<li>") {
		t.Errorf("expected <ul> and <li> tags for bullet list")
	}
	if !strings.Contains(html, "<ol>") {
		t.Errorf("expected <ol> tag for numbered list")
	}
	if !strings.Contains(html, "<table") || !strings.Contains(html, "</table>") {
		t.Errorf("expected complete <table>...</table> tag")
	}
}

func TestCleanToSemanticHTMLPreservesCodeAndRepairsTruncatedTable(t *testing.T) {
	input := `<p># Titre Principal Dans Paragraphe</p>
<p>Voici du texte explicatif.</p>
<pre><code class="language-python">
# Commentaire Python qui ne doit PAS devenir un titre h2
def calculate_bayes(p, l):
    # Second commentaire Python
    return p * l
</code></pre>
<p>## Section Suivante</p>
<p>Texte intermédiaire.</p>
<table>
<thead>
<tr>
<th>Col 1</th>
<th>Col 2</th>
` // Truncated table without closing tags

	html := CleanToSemanticHTML(input, "Test Titre")

	// Must NOT contain # outside pre
	if strings.Contains(html, "# Titre Principal") {
		t.Errorf("expected # Titre Principal to be converted to h2, got:\n%s", html)
	}
	if !strings.Contains(html, "<h2>Titre Principal Dans Paragraphe</h2>") {
		t.Errorf("expected h2 tag for Titre Principal, got:\n%s", html)
	}

	// Must preserve Python comments inside <pre><code>
	if !strings.Contains(html, "# Commentaire Python qui ne doit PAS devenir un titre h2") {
		t.Errorf("expected Python comment inside pre/code to remain intact, got:\n%s", html)
	}
	if strings.Contains(html, "<h2>Commentaire Python") {
		t.Errorf("Python comment should not be converted to h2 tag")
	}

	// Must repair truncated table into a valid complete table
	if !strings.Contains(html, "</table>") {
		t.Errorf("expected complete <table>...</table> tag after repair")
	}
	if !strings.Contains(html, "<tbody>") {
		t.Errorf("expected <tbody> tag in repaired table")
	}

	// Must NOT hallucinate cognitive science terms in unrelated topics
	if strings.Contains(html, "Traitement Prédictif") || strings.Contains(html, "Réalité Monitoring") {
		t.Errorf("expected no cognitive science hallucination in fallback table")
	}
	if !strings.Contains(html, "Références et Sources Documentaires") {
		t.Errorf("expected references and documentary sources section")
	}
}

func TestCleanIncompleteSentences(t *testing.T) {
	input := `<p>La convection lithosphérique influence la tectonique des plaques. Bien que les effets sur la tectonique</p>
<p>Autre paragraphe complet et bien clos.</p>
<p>Tandis que</p>`

	cleaned := CleanIncompleteSentences(input)

	if strings.Contains(cleaned, "Bien que les effets sur la tectonique") {
		t.Errorf("expected truncated dangling sentence to be removed, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "La convection lithosphérique influence la tectonique des plaques.") {
		t.Errorf("expected valid preceding sentence to be preserved, got:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "Tandis que") {
		t.Errorf("expected isolated dangling paragraph to be removed, got:\n%s", cleaned)
	}
}

func TestPurgeGenericPlaceholderTables(t *testing.T) {
	input := `<div class="table-responsive">
<table>
<tr><td>Documentation Officielle &amp; Spécifications</td><td>Spécification technique / RFC / Standard</td></tr>
</table>
</div>`

	cleaned := PurgeGenericPlaceholderTables(input, "L'Échafaudage Invisible : Dynamique Glaciaire")

	if strings.Contains(cleaned, "RFC / Standard") {
		t.Errorf("expected RFC placeholder to be purged for climate article, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Rapports GIEC / Copernicus") {
		t.Errorf("expected GIEC/Copernicus replacement for climate article, got:\n%s", cleaned)
	}
}

func TestSanitizeDeepContentBiotechAndKyoto(t *testing.T) {
	input := `<p>Des essais cliniques, soutenus par des entreprises comme ExaCell Therapeutics, ont montré des résultats encourageants.</p>
<p>Des projets menés par le laboratoire de Shinya Yamanaka à l'Institut RIKEN et de Masayo Takahashi à l'Université du Japon à Kyoto ont démontré la faisabilité.</p>`

	cleaned := SanitizeDeepContent(input, "L'Architecte de la Mémoire Synthétique")

	if strings.Contains(cleaned, "ExaCell Therapeutics") {
		t.Errorf("expected ExaCell Therapeutics to be purged, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Vertex Pharmaceuticals et CRISPR Therapeutics") {
		t.Errorf("expected Vertex and CRISPR Therapeutics to be present, got:\n%s", cleaned)
	}
	if strings.Contains(cleaned, "Université du Japon à Kyoto") {
		t.Errorf("expected 'Université du Japon à Kyoto' to be purged, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "Université de Kyoto") {
		t.Errorf("expected 'Université de Kyoto' to be present, got:\n%s", cleaned)
	}
}

func TestSanitizeDeepContentBaseEditingAndStoica(t *testing.T) {
	input := `<p>Les technologies d'édition génomique comme les variants "base editing" et "prime editing", permettent de modifier des séquences d'ADN en minimisant les dommages à l'ADN par rapport aux systèmes de coupure classique (CRISPR-Cas9), tout en nécessitant des cassures double-brin transitoires lors de l'activation de la désoxyribonucléase.</p>
<table>
<tr><td>Stoica, M. et al. (2016)</td><td>Nature Biotechnology</td><td>Article de recherche</td><td>Mémoire synthétique <a href="https://doi.org/10.1038/nbt.3529">DOI: 10.1038/nbt.3529</a></td></tr>
</table>`

	cleaned := SanitizeDeepContent(input, "L'Architecte de la Mémoire Synthétique")

	// Must NOT claim that base editing requires double-strand breaks
	if strings.Contains(cleaned, "nécessitant des cassures double-brin") {
		t.Errorf("expected contradiction about needing DSBs to be eliminated, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "sans induire de cassure double-brin") {
		t.Errorf("expected 'sans induire de cassure double-brin' to be asserted, got:\n%s", cleaned)
	}

	// Must NOT keep Delaware law DOI 10.1038/nbt.3529
	if strings.Contains(cleaned, "10.1038/nbt.3529") {
		t.Errorf("expected Delaware law DOI to be replaced, got:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, "10.1126/science.aab2956") {
		t.Errorf("expected real seminal paper Bintu et al. DOI to be present, got:\n%s", cleaned)
	}
}
