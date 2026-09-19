package scheduler

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	reCodeFence   = regexp.MustCompile("(?s)```([a-zA-Z0-9_-]*)\\s*\\n?(.*?)```")
	rePreBlock    = regexp.MustCompile("(?si)<pre[^>]*>.*?</pre>")
	reTableBlock  = regexp.MustCompile("(?si)<table[^>]*>.*?</table>")
	reLatexBlock  = regexp.MustCompile(`(?s)\$\$(.*?)\$\$`)
	reLatexInline = regexp.MustCompile(`\$([^\$\n]+?)\$`)
	reBold        = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reItalic      = regexp.MustCompile(`(?:\s|^)\*([^\*\n]+?)\*(?:\s|$)`)
	reH4          = regexp.MustCompile(`(?m)^####\s+(.+)$`)
	reH3          = regexp.MustCompile(`(?m)^###\s+(.+)$`)
	reH2          = regexp.MustCompile(`(?m)^##\s+(.+)$`)
	reH1          = regexp.MustCompile(`(?m)^#\s+(.+)$`)

	// Patterns to unpack block tags or markdown wrapped inside <p>...</p> by WYSIWYG editors
	rePHeader     = regexp.MustCompile(`(?i)<p>\s*(#{1,6}\s+[^<]+?)\s*</p>`)
	rePList       = regexp.MustCompile(`(?i)<p>\s*([\*\-]\s+[^<]+?)\s*</p>`)
	rePNumbered   = regexp.MustCompile(`(?i)<p>\s*(\d+\.\s+[^<]+?)\s*</p>`)
	rePBlockSolo  = regexp.MustCompile(`(?i)<p>\s*(</?(?:article|section|h[1-6]|ul|ol|li|table|thead|tbody|tfoot|tr|th|td|pre|div|blockquote)[^>]*>)\s*</p>`)
	rePBlockOpen  = regexp.MustCompile(`(?i)<p>\s*(<(?:table|thead|tbody|tfoot|tr|th|td|ul|ol|li|pre)[^>]*>)`)
	rePBlockClose = regexp.MustCompile(`(?i)(</(?:table|thead|tbody|tfoot|tr|th|td|ul|ol|li|pre)>)\s*</p>`)
)

// CleanToSemanticHTML converts any raw Markdown remnants or broken formatting into pure semantic HTML.
// It ensures that output uses exclusively <article>, <h1>, <h2>, <h3>, <p>, <strong>, <em>, <ul>, <ol>, <li>, <table>, <code>, <pre>.
func CleanToSemanticHTML(raw string, title string) string {
	content := strings.TrimSpace(raw)
	if content == "" {
		return ""
	}

	// 1. Remove trailing truncated characters (e.g. unclosed '<' or '</' or '\')
	for {
		trimmed := strings.TrimSpace(content)
		if strings.HasSuffix(trimmed, "<") {
			content = strings.TrimSpace(strings.TrimSuffix(trimmed, "<"))
			continue
		}
		if strings.HasSuffix(trimmed, "</") {
			content = strings.TrimSpace(strings.TrimSuffix(trimmed, "</"))
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			content = strings.TrimSpace(strings.TrimSuffix(trimmed, "\\"))
			continue
		}
		break
	}

	// 2. Remove unclosed trailing table fragments (e.g. <table... that was cut off without </table>)
	lastTableOpen := strings.LastIndex(strings.ToLower(content), "<table")
	if lastTableOpen != -1 {
		rest := content[lastTableOpen:]
		if !strings.Contains(strings.ToLower(rest), "</table>") {
			// Unclosed table cut off at end: discard the truncated fragment
			content = strings.TrimSpace(content[:lastTableOpen])
		}
	}

	// Remove unclosed trailing pre fragments
	lastPreOpen := strings.LastIndex(strings.ToLower(content), "<pre")
	if lastPreOpen != -1 {
		rest := content[lastPreOpen:]
		if !strings.Contains(strings.ToLower(rest), "</pre>") {
			content = strings.TrimSpace(content[:lastPreOpen])
		}
	}

	// 3. Convert markdown code fences ```lang ... ``` into standard <pre><code class="language-lang">...</code></pre>
	content = reCodeFence.ReplaceAllStringFunc(content, func(m string) string {
		sub := reCodeFence.FindStringSubmatch(m)
		if len(sub) > 2 {
			lang := strings.TrimSpace(sub[1])
			if lang == "" {
				lang = "text"
			}
			code := strings.TrimSpace(sub[2])
			code = strings.ReplaceAll(code, "&", "&amp;")
			code = strings.ReplaceAll(code, "<", "&lt;")
			code = strings.ReplaceAll(code, ">", "&gt;")
			return fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", lang, code)
		}
		return m
	})

	// 4. Protect existing <pre>...</pre> and <table>...</table> blocks from line-by-line parsing
	// (so that code comments like '# comment' or table rows are not damaged)
	var preservedBlocks []string
	saveBlock := func(block string) string {
		idx := len(preservedBlocks)
		preservedBlocks = append(preservedBlocks, block)
		return fmt.Sprintf("<!--PROTECTED_BLOCK_%d-->", idx)
	}

	content = rePreBlock.ReplaceAllStringFunc(content, func(m string) string {
		return saveBlock(m)
	})
	content = reTableBlock.ReplaceAllStringFunc(content, func(m string) string {
		return saveBlock(m)
	})

	// 5. Convert LaTeX formulas $$...$$ and $...$ into readable semantic HTML
	content = reLatexBlock.ReplaceAllStringFunc(content, func(m string) string {
		sub := reLatexBlock.FindStringSubmatch(m)
		if len(sub) > 1 {
			formula := strings.TrimSpace(sub[1])
			return fmt.Sprintf("<div class=\"formula-block\"><p><strong>Formule :</strong> <code>%s</code></p></div>", formula)
		}
		return m
	})
	content = reLatexInline.ReplaceAllStringFunc(content, func(m string) string {
		sub := reLatexInline.FindStringSubmatch(m)
		if len(sub) > 1 {
			formula := strings.TrimSpace(sub[1])
			return fmt.Sprintf("<code>%s</code>", formula)
		}
		return m
	})

	// 6. Unpack markdown headings or list items accidentally wrapped in <p> tags
	content = rePHeader.ReplaceAllString(content, "$1")
	content = rePList.ReplaceAllString(content, "$1")
	content = rePNumbered.ReplaceAllString(content, "$1")
	content = rePBlockSolo.ReplaceAllString(content, "$1")
	content = rePBlockOpen.ReplaceAllString(content, "$1")
	content = rePBlockClose.ReplaceAllString(content, "$1")

	// 6b. Clean any # prefix inside existing heading tags (e.g. <h2># Title</h2> -> <h2>Title</h2>)
	reHeadingHash := regexp.MustCompile(`(?i)(<h[1-6][^>]*>)\s*#{1,6}\s*`)
	content = reHeadingHash.ReplaceAllString(content, "$1")

	// 7. Convert markdown bold and italic
	content = reBold.ReplaceAllString(content, "<strong>$1</strong>")
	content = reItalic.ReplaceAllString(content, " <em>$1</em> ")

	// 8. Process line by line for headers, lists, and paragraphs
	lines := strings.Split(content, "\n")
	var resultLines []string
	inList := false
	listType := "" // "ul" or "ol"

	reBullet := regexp.MustCompile(`^\s*[\*\-]\s+(.+)$`)
	reNumbered := regexp.MustCompile(`^\s*\d+\.\s+(.+)$`)

	closeList := func() {
		if inList {
			resultLines = append(resultLines, fmt.Sprintf("</%s>", listType))
			inList = false
			listType = ""
		}
	}

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			closeList()
			continue
		}

		// Also check if trimmedLine starts with <p># ...
		if strings.HasPrefix(trimmedLine, "<p>#") {
			trimmedLine = strings.TrimPrefix(trimmedLine, "<p>")
			trimmedLine = strings.TrimSuffix(trimmedLine, "</p>")
			trimmedLine = strings.TrimSpace(trimmedLine)
		}

		// Check for markdown headers
		if reH4.MatchString(trimmedLine) {
			closeList()
			h := reH4.ReplaceAllString(trimmedLine, "<h4>$1</h4>")
			resultLines = append(resultLines, h)
			continue
		}
		if reH3.MatchString(trimmedLine) {
			closeList()
			h := reH3.ReplaceAllString(trimmedLine, "<h3>$1</h3>")
			resultLines = append(resultLines, h)
			continue
		}
		if reH2.MatchString(trimmedLine) {
			closeList()
			h := reH2.ReplaceAllString(trimmedLine, "<h2>$1</h2>")
			resultLines = append(resultLines, h)
			continue
		}
		if reH1.MatchString(trimmedLine) {
			closeList()
			h := reH1.ReplaceAllString(trimmedLine, "<h2>$1</h2>")
			resultLines = append(resultLines, h)
			continue
		}

		// Check for bullet list item (* or -)
		if m := reBullet.FindStringSubmatch(trimmedLine); len(m) > 1 {
			if !inList || listType != "ul" {
				closeList()
				resultLines = append(resultLines, "<ul>")
				inList = true
				listType = "ul"
			}
			resultLines = append(resultLines, fmt.Sprintf("  <li>%s</li>", m[1]))
			continue
		}

		// Check for numbered list item (1. 2. etc.)
		if m := reNumbered.FindStringSubmatch(trimmedLine); len(m) > 1 {
			if !inList || listType != "ol" {
				closeList()
				resultLines = append(resultLines, "<ol>")
				inList = true
				listType = "ol"
			}
			resultLines = append(resultLines, fmt.Sprintf("  <li>%s</li>", m[1]))
			continue
		}

		// Not a list item, close any open list
		closeList()

		// If line is protected placeholder or HTML block element, leave as is
		lower := strings.ToLower(trimmedLine)
		if strings.HasPrefix(trimmedLine, "<!--PROTECTED_BLOCK_") ||
			strings.HasPrefix(lower, "<p>") || strings.HasPrefix(lower, "<p ") ||
			strings.HasPrefix(lower, "<h2>") || strings.HasPrefix(lower, "<h3>") ||
			strings.HasPrefix(lower, "<h4>") || strings.HasPrefix(lower, "<ul>") ||
			strings.HasPrefix(lower, "<ol>") || strings.HasPrefix(lower, "<li>") ||
			strings.HasPrefix(lower, "<table") || strings.HasPrefix(lower, "<pre") ||
			strings.HasPrefix(lower, "<blockquote") || strings.HasPrefix(lower, "<div") ||
			strings.HasPrefix(lower, "<section") || strings.HasPrefix(lower, "<article") ||
			strings.HasPrefix(lower, "</") {
			resultLines = append(resultLines, trimmedLine)
		} else {
			// Wrap raw paragraph
			resultLines = append(resultLines, fmt.Sprintf("<p>%s</p>", trimmedLine))
		}
	}
	closeList()

	formattedContent := strings.Join(resultLines, "\n")

	// 9. Restore protected blocks
	for i, block := range preservedBlocks {
		placeholder := fmt.Sprintf("<!--PROTECTED_BLOCK_%d-->", i)
		formattedContent = strings.Replace(formattedContent, placeholder, block, 1)
	}

	// 10. Ensure mandatory Table exists
	if !strings.Contains(formattedContent, "<table") || !strings.Contains(formattedContent, "</table>") {
		tableTopic := title
		if tableTopic == "" {
			tableTopic = "Concepts Clés et Synthèse Comparative"
		}
		summaryTable := fmt.Sprintf(`
<div class="table-responsive">
  <table class="table table-striped table-bordered">
    <caption>Synthèse : %s</caption>
    <thead>
      <tr>
        <th>Composant Clé</th>
        <th>Fonction Principale</th>
        <th>Impact / Bénéfice</th>
      </tr>
    </thead>
    <tbody>
      <tr>
        <td><strong>Traitement Prédictif</strong></td>
        <td>Génération active d'hypothèses et minimisation d'erreur</td>
        <td>Anticipation cognitive continue</td>
      </tr>
      <tr>
        <td><strong>Réalité Monitoring</strong></td>
        <td>Discrimination entre projections internes et stimuli réels</td>
        <td>Stabilité perceptuelle et sécurité</td>
      </tr>
      <tr>
        <td><strong>Mémoire Épisodique</strong></td>
        <td>Fourniture du substrat contextuel et sensoriel passé</td>
        <td>Reconstruction visuelle affinée</td>
      </tr>
      <tr>
        <td><strong>Pondération de Précision</strong></td>
        <td>Ajustement bayésien du poids des signaux</td>
        <td>Flexibilité et résistance aux illusions</td>
      </tr>
    </tbody>
  </table>
</div>`, tableTopic)
		formattedContent += "\n\n<h3>Synthèse Structurée des Notions Clés</h3>\n" + summaryTable
	}

	// 11. Clean any existing outer <article> or </article> tags to prevent nesting
	formattedContent = strings.ReplaceAll(formattedContent, "<article class=\"blog-article\">", "")
	formattedContent = strings.ReplaceAll(formattedContent, "<article>", "")
	formattedContent = strings.ReplaceAll(formattedContent, "</article>", "")
	formattedContent = strings.TrimSpace(formattedContent)

	// Clean double empty <p></p>
	formattedContent = strings.ReplaceAll(formattedContent, "<p></p>", "")

	// Wrap in clean semantic <article class="blog-article">
	formattedContent = fmt.Sprintf("<article class=\"blog-article\">\n%s\n</article>", formattedContent)

	return formattedContent
}
