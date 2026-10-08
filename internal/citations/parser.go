package citations

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

var (
	reTable          = regexp.MustCompile(`(?is)<table\b[^>]*>.*?</table>`)
	reTHead          = regexp.MustCompile(`(?is)<thead\b[^>]*>.*?</thead>`)
	reTBody          = regexp.MustCompile(`(?is)<tbody\b[^>]*>(.*?)</tbody>`)
	reTR             = regexp.MustCompile(`(?is)<tr\b[^>]*>.*?</tr>`)
	reTH             = regexp.MustCompile(`(?is)<th\b`)
	reTD             = regexp.MustCompile(`(?is)<td\b[^>]*>(.*?)</td>`)
	reAnyTag         = regexp.MustCompile(`(?s)<[^>]*>`)
	reYear           = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reDOI            = regexp.MustCompile(`(?i)\b(10\.\d{4,9}/[-._;()/:A-Za-z0-9]+)\b`)
	reRefSection     = regexp.MustCompile(`(?is)<(?:div|section)\b[^>]*class="[^"]*(?:reference|source|biblio)[^"]*"[^>]*>(.*?)</(?:div|section)>`)
	reRefHeader      = regexp.MustCompile(`(?is)<h[2-4]\b[^>]*>.*?(?:référence|reference|bibliographie|sources).*?</h[2-4]>`)
	reLI             = regexp.MustCompile(`(?is)<li\b[^>]*>(.*?)</li>`)
	reAuthorYear     = regexp.MustCompile(`(?i)([A-ZÀ-ÖØ-öø-ÿ][a-zà-öø-ÿ]+(?:\s+[A-ZÀ-ÖØ-öø-ÿ]\.?)?(?:(?:\s*(?:,|&|et|and)\s*)[A-ZÀ-ÖØ-öø-ÿ][a-zà-öø-ÿ]+(?:\s+[A-ZÀ-ÖØ-öø-ÿ]\.?)?)*(?:\s+et\s+al\.?)?)\s*(?:\((\d{4})\)|,\s*(\d{4}))`)
	reEtAl           = regexp.MustCompile(`(?i)\bet\s+al\.?\b`)
	reCommonJournals = regexp.MustCompile(`(?i)\b(Nature(?:\s+[A-Za-z]+)?|Science(?:\s+[A-Za-z]+)?|Cell(?:\s+[A-Za-z]+)?|PNAS|Proceedings of the National Academy of Sciences|The Lancet|Lancet|PLOS\s+[A-Za-z]+|Nucleic Acids Res(?:earch)?|JAMA|New England Journal of Medicine|NEJM|EMBO Journal|Molecular Cell|Physical Review(?:\s+[A-Za-z]+)?|Phys\.?\s*Rev\.?|Geophysical Research Letters|GRL|Journal of Climate)\b`)
)

// StripHTML converts HTML string into clean plain text.
func StripHTML(s string) string {
	plain := reAnyTag.ReplaceAllString(s, " ")
	plain = html.UnescapeString(plain)
	return strings.Join(strings.Fields(plain), " ")
}

// IsReferenceTable checks if a table element represents a bibliographic or references table.
func IsReferenceTable(tableHTML string) bool {
	lower := strings.ToLower(tableHTML)
	if strings.Contains(lower, "doi.org") || strings.Contains(lower, "doi:") {
		return true
	}
	head := reTHead.FindString(tableHTML)
	if head == "" {
		if rows := reTR.FindAllString(tableHTML, 1); len(rows) > 0 {
			head = rows[0]
		}
	}
	h := strings.ToLower(StripHTML(head))
	return strings.Contains(h, "référence") || strings.Contains(h, "reference") ||
		strings.Contains(h, "source") || strings.Contains(h, "revue") || strings.Contains(h, "journal") ||
		strings.Contains(h, "bibliographie")
}

// ParseCitations extracts all bibliographic citations from HTML content (tables, lists, and reference sections).
func ParseCitations(content string) []ParsedCitation {
	var citations []ParsedCitation
	idx := 1

	// 1. Extract from reference tables
	for _, table := range reTable.FindAllString(content, -1) {
		if !IsReferenceTable(table) {
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
			cells := extractCells(tr)
			if len(cells) == 0 {
				continue
			}
			plainRow := StripHTML(strings.Join(cells, " — "))
			if len(plainRow) < 5 {
				continue
			}

			parsed := parseTableRowCitation(idx, tr, cells)
			citations = append(citations, parsed)
			idx++
		}
	}

	// 2. Extract from reference lists (<div class="references-section">...<ul><li>...)
	for _, sec := range reRefSection.FindAllStringSubmatch(content, -1) {
		for _, li := range reLI.FindAllString(sec[1], -1) {
			plain := StripHTML(li)
			if len(plain) < 10 {
				continue
			}
			parsed := parseTextCitation(idx, "list_item", li, plain)
			citations = append(citations, parsed)
			idx++
		}
	}

	// 3. Extract from reference header sections (<h2..>Références</h2>...)
	for _, loc := range reRefHeader.FindAllStringIndex(content, -1) {
		endHeader := loc[1]
		rest := content[endHeader:]
		reNextH := regexp.MustCompile(`(?i)<h[1-4]\b`)
		if nextLoc := reNextH.FindStringIndex(rest); nextLoc != nil {
			rest = rest[:nextLoc[0]]
		}
		if !strings.Contains(rest, "<table") {
			for _, li := range reLI.FindAllString(rest, -1) {
				plain := StripHTML(li)
				if len(plain) < 10 {
					continue
				}
				parsed := parseTextCitation(idx, "list_item", li, plain)
				citations = append(citations, parsed)
				idx++
			}
		}
	}

	return citations
}

func extractCells(tr string) []string {
	var cells []string
	matches := reTD.FindAllStringSubmatch(tr, -1)
	for _, m := range matches {
		if len(m) > 1 {
			cells = append(cells, strings.TrimSpace(StripHTML(m[1])))
		}
	}
	return cells
}

func parseTableRowCitation(index int, trHTML string, cells []string) ParsedCitation {
	rawText := strings.Join(cells, " — ")
	pc := ParsedCitation{
		Index:      index,
		SourceType: "table_row",
		RawText:    rawText,
		Original:   trHTML,
	}

	// Search for DOI
	if doiMatch := reDOI.FindString(rawText); doiMatch != "" {
		pc.DOI = doiMatch
	}

	// Extract Year
	if yMatch := reYear.FindString(rawText); yMatch != "" {
		if y, err := strconv.Atoi(yMatch); err == nil {
			pc.Year = y
		}
	}

	// In typical Pixel tables:
	// Cell 0: Reference / Author (e.g. "Yamanaka, S. et Thomson, J. H. (2014)" or "Ernst, J. et al. (2015)")
	// Cell 1: Journal / Source (e.g. "Cell", "Nature", "Université de Stanford")
	// Cell 2: Type (e.g. "Article de recherche", "Base de données technique")
	// Cell 3: Description (e.g. "Protocoles de reprogrammation...")
	if len(cells) > 0 {
		pc.Authors = extractAuthors(cells[0])
		if pc.Year == 0 {
			if yMatch := reYear.FindString(cells[0]); yMatch != "" {
				pc.Year, _ = strconv.Atoi(yMatch)
			}
		}
	}

	if len(cells) > 1 {
		jCandidate := cells[1]
		if reCommonJournals.MatchString(jCandidate) {
			pc.Journal = reCommonJournals.FindString(jCandidate)
		} else {
			pc.Journal = jCandidate
		}
	}

	if len(cells) > 3 {
		pc.TopicDesc = cells[3]
		pc.Title = cells[3]
	} else if len(cells) > 2 {
		pc.TopicDesc = cells[2]
		if pc.Title == "" {
			pc.Title = cells[2]
		}
	}

	// If journal wasn't found in cell 1, look across whole row
	if pc.Journal == "" {
		if jMatch := reCommonJournals.FindString(rawText); jMatch != "" {
			pc.Journal = jMatch
		}
	}

	return pc
}

func parseTextCitation(index int, sourceType, originalHTML, plainText string) ParsedCitation {
	pc := ParsedCitation{
		Index:      index,
		SourceType: sourceType,
		RawText:    plainText,
		Original:   originalHTML,
	}

	if doiMatch := reDOI.FindString(plainText); doiMatch != "" {
		pc.DOI = doiMatch
	}

	if yMatch := reYear.FindString(plainText); yMatch != "" {
		pc.Year, _ = strconv.Atoi(yMatch)
	}

	if jMatch := reCommonJournals.FindString(plainText); jMatch != "" {
		pc.Journal = jMatch
	}

	// Try author-year regex
	if m := reAuthorYear.FindStringSubmatch(plainText); len(m) > 1 {
		pc.Authors = extractAuthors(m[1])
	} else {
		// Fallback: take leading segment before year or parenthetical
		parts := strings.Split(plainText, "(")
		if len(parts) > 1 && len(parts[0]) > 3 {
			pc.Authors = extractAuthors(parts[0])
		}
	}

	// Look for title between quotes
	reQuotes := regexp.MustCompile(`[«"“]([^»"”]{8,})[»"”]`)
	if q := reQuotes.FindStringSubmatch(plainText); len(q) > 1 {
		pc.Title = strings.TrimSpace(q[1])
	} else {
		pc.TopicDesc = plainText
	}

	return pc
}

// extractAuthors extracts clean author surnames from a string like "Yamanaka, S. et Thomson, J. H."
func extractAuthors(s string) []string {
	// Strip "et al."
	clean := reEtAl.ReplaceAllString(s, "")
	clean = strings.TrimSpace(clean)

	// Remove trailing year in parens like (2014)
	reYearParen := regexp.MustCompile(`\s*\(\s*\d{4}\s*\)`)
	clean = reYearParen.ReplaceAllString(clean, "")

	// Split by delimiters
	delims := regexp.MustCompile(`(?i)\s*(?:,|&|\bet\b|\band\b|;)\s*`)
	tokens := delims.Split(clean, -1)

	var authors []string
	seen := map[string]bool{}

	reSurname := regexp.MustCompile(`\b([A-ZÀ-ÖØ-öø-ÿ][A-Za-zà-öø-ÿ'-]{1,})\b`)
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" || strings.EqualFold(tok, "al") {
			continue
		}
		// Surnames usually come first in "Yamanaka, S." or are capital words
		matches := reSurname.FindAllString(tok, -1)
		for _, m := range matches {
			mLower := strings.ToLower(m)
			// Filter out stop words
			if mLower == "lab" || mLower == "laboratoire" || mLower == "database" ||
				mLower == "consortium" || mLower == "institute" || mLower == "institut" ||
				mLower == "cell" || mLower == "nature" || mLower == "science" || mLower == "article" {
				continue
			}
			if !seen[mLower] {
				seen[mLower] = true
				authors = append(authors, m)
			}
		}
	}

	return authors
}
