package citations

import "strings"

// MatchStatus represents the outcome of an automated bibliographic check.
type MatchStatus string

const (
	StatusValidated     MatchStatus = "VALIDATED"     // Confirmed authentic paper with official DOI/PMID
	StatusChimera       MatchStatus = "CHIMERA"       // Detected artificial author pairing, fake journal, or impossible year
	StatusNotFound      MatchStatus = "NOT_FOUND"     // No corresponding paper in Crossref or PubMed
	StatusInstitutional MatchStatus = "INSTITUTIONAL" // Recognized official body (GIEC, NIH, OMS, etc.)
	StatusCorrected     MatchStatus = "CORRECTED"     // Successfully re-mapped to the authentic canonical paper
	StatusRemoved       MatchStatus = "REMOVED"       // Pure hallucination removed from output
)

// ParsedCitation contains the extracted components of a bibliographic citation.
type ParsedCitation struct {
	Index      int      `json:"index"`
	SourceType string   `json:"source_type"` // "table_row", "list_item", "paragraph"
	RawText    string   `json:"raw_text"`    // Plain text representation
	Original   string   `json:"original"`    // Exact HTML snippet (tr, li, or p)
	Authors    []string `json:"authors"`     // Extracted author surnames
	Year       int      `json:"year"`        // Publication year
	Title      string   `json:"title"`       // Article or study title
	Journal    string   `json:"journal"`     // Journal, review or container name
	DOI        string   `json:"doi"`         // Existing DOI if already present
	TopicDesc  string   `json:"topic_desc"`  // Description / topic column or note
}

// ValidationResult records the findings of the Crossref and PubMed APIs for a single citation.
type ValidationResult struct {
	Citation        ParsedCitation `json:"citation"`
	Status          MatchStatus    `json:"status"`
	Confidence      float64        `json:"confidence"` // 0.0 to 1.0
	ValidatedDOI    string         `json:"validated_doi,omitempty"`
	DOIURL          string         `json:"doi_url,omitempty"`
	PMID            string         `json:"pmid,omitempty"`
	ActualTitle     string         `json:"actual_title,omitempty"`
	ActualAuthors   []string       `json:"actual_authors,omitempty"`
	ActualJournal   string         `json:"actual_journal,omitempty"`
	ActualYear      int            `json:"actual_year,omitempty"`
	SourceAPI       string         `json:"source_api,omitempty"` // "Crossref", "PubMed", "Institutional"
	Reason          string         `json:"reason"`
	ReplacementHTML string         `json:"replacement_html,omitempty"`
}

// CitationAuditReport summarizes the automated validation of citations in an article.
type CitationAuditReport struct {
	TotalScanned     int                `json:"total_scanned"`
	Validated        int                `json:"validated"`
	InjectedDOIs     int                `json:"injected_dois"`
	ChimerasRejected int                `json:"chimeras_rejected"`
	Corrected        int                `json:"corrected"`
	Removed          int                `json:"removed"`
	Institutional    int                `json:"institutional"`
	Results          []ValidationResult `json:"results"`
	LogEntries       []string           `json:"log_entries,omitempty"`
}

func (r *CitationAuditReport) TotalAdjustments() int {
	if r == nil {
		return 0
	}
	return r.InjectedDOIs + r.ChimerasRejected + r.Corrected + r.Removed
}

// CleanDOIPrefix strips URI protocols and 'doi:' prefixes from a DOI string.
func CleanDOIPrefix(doi string) string {
	clean := strings.TrimPrefix(doi, "https://doi.org/")
	clean = strings.TrimPrefix(clean, "http://doi.org/")
	clean = strings.TrimPrefix(clean, "doi:")
	clean = strings.TrimPrefix(clean, "DOI:")
	clean = strings.TrimPrefix(clean, "/")
	return strings.TrimSpace(clean)
}
