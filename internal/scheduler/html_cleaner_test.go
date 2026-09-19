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
}
