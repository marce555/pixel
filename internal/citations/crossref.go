package citations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultCrossrefBaseURL = "https://api.crossref.org/works"

// CrossrefItem represents a work item returned by Crossref.
type CrossrefItem struct {
	Title          []string `json:"title"`
	DOI            string   `json:"DOI"`
	ContainerTitle []string `json:"container-title"`
	Author         []struct {
		Given  string `json:"given"`
		Family string `json:"family"`
	} `json:"author"`
	Issued struct {
		DateParts [][]int `json:"date-parts"`
	} `json:"issued"`
	Score float64 `json:"score"`
}

type crossrefResponse struct {
	Status  string `json:"status"`
	Message struct {
		TotalResults int            `json:"total-results"`
		Items        []CrossrefItem `json:"items"`
	} `json:"message"`
}

type crossrefSingleResponse struct {
	Status  string       `json:"status"`
	Message CrossrefItem `json:"message"`
}

// CrossrefClient queries the Crossref REST API using the Polite Pool.
type CrossrefClient struct {
	httpClient *http.Client
	baseURL    string
	mailto     string
}

// NewCrossrefClient initializes a CrossrefClient with a polite mailto header.
func NewCrossrefClient(mailto string) *CrossrefClient {
	if mailto == "" {
		if envMail := os.Getenv("CROSSREF_MAILTO"); envMail != "" {
			mailto = envMail
		} else {
			mailto = "contact@appliyou.fr"
		}
	}

	return &CrossrefClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    defaultCrossrefBaseURL,
		mailto:     mailto,
	}
}

// SetBaseURL overrides the base URL (primarily for testing with HTTP mocks).
func (c *CrossrefClient) SetBaseURL(url string) {
	c.baseURL = url
}

// QueryBibliographic queries Crossref with a bibliographic string or citation elements.
func (c *CrossrefClient) QueryBibliographic(ctx context.Context, pc ParsedCitation) ([]CrossrefItem, error) {
	// If a DOI is already specified, try direct resolution first
	if pc.DOI != "" {
		item, err := c.GetByDOI(ctx, pc.DOI)
		if err == nil && item != nil {
			return []CrossrefItem{*item}, nil
		}
	}

	// Construct query terms
	var queryParts []string
	if len(pc.Authors) > 0 {
		queryParts = append(queryParts, strings.Join(pc.Authors, " "))
	}
	if pc.Year > 0 {
		queryParts = append(queryParts, fmt.Sprintf("%d", pc.Year))
	}
	if pc.Journal != "" {
		queryParts = append(queryParts, pc.Journal)
	}
	if pc.Title != "" {
		queryParts = append(queryParts, pc.Title)
	}

	queryTerm := strings.Join(queryParts, " ")
	if strings.TrimSpace(queryTerm) == "" {
		queryTerm = pc.RawText
	}

	reqURL := fmt.Sprintf("%s?query.bibliographic=%s&rows=5", c.baseURL, url.QueryEscape(queryTerm))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	// Polite pool header
	req.Header.Set("User-Agent", fmt.Sprintf("PixelBot/1.0 (mailto:%s)", c.mailto))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("crossref request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("crossref HTTP %d: %s", resp.StatusCode, string(body))
	}

	var crResp crossrefResponse
	if err := json.NewDecoder(resp.Body).Decode(&crResp); err != nil {
		return nil, fmt.Errorf("failed to decode crossref response: %w", err)
	}

	return crResp.Message.Items, nil
}

// GetByDOI fetches a specific work by DOI.
func (c *CrossrefClient) GetByDOI(ctx context.Context, doi string) (*CrossrefItem, error) {
	cleanDOI := CleanDOIPrefix(doi)
	if cleanDOI == "" {
		return nil, fmt.Errorf("empty DOI")
	}

	reqURL := fmt.Sprintf("%s/%s", c.baseURL, url.PathEscape(cleanDOI))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("PixelBot/1.0 (mailto:%s)", c.mailto))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("crossref DOI HTTP %d", resp.StatusCode)
	}

	var single crossrefSingleResponse
	if err := json.NewDecoder(resp.Body).Decode(&single); err != nil {
		return nil, err
	}
	return &single.Message, nil
}

// EvaluateMatch compares a ParsedCitation with Crossref results to detect validations or chimeras.
func EvaluateMatch(pc ParsedCitation, items []CrossrefItem) (status MatchStatus, bestItem *CrossrefItem, confidence float64, reason string) {
	if len(items) == 0 {
		return StatusNotFound, nil, 0.0, "Aucun résultat correspondant dans Crossref."
	}

	claimedAuthorsLower := make([]string, len(pc.Authors))
	for i, a := range pc.Authors {
		claimedAuthorsLower[i] = strings.ToLower(strings.TrimSpace(a))
	}

	claimedJournalLower := strings.ToLower(strings.TrimSpace(pc.Journal))

	var bestScore float64
	var candidate *CrossrefItem
	hasPartialAuthorMatch := false

	for i := range items {
		item := &items[i]
		actualAuthors := extractItemAuthors(item)
		actualJournal := extractItemJournal(item)
		actualYear := extractItemYear(item)

		authorMatches := 0
		for _, claimed := range claimedAuthorsLower {
			for _, act := range actualAuthors {
				if strings.Contains(act, claimed) || strings.Contains(claimed, act) {
					authorMatches++
					break
				}
			}
		}

		journalMatch := false
		if claimedJournalLower != "" && actualJournal != "" {
			actJLower := strings.ToLower(actualJournal)
			if strings.Contains(actJLower, claimedJournalLower) || strings.Contains(claimedJournalLower, actJLower) {
				journalMatch = true
			}
		}

		yearDiff := 999
		if pc.Year > 0 && actualYear > 0 {
			if pc.Year > actualYear {
				yearDiff = pc.Year - actualYear
			} else {
				yearDiff = actualYear - pc.Year
			}
		}

		// Calculate score
		score := 0.0
		totalClaimed := len(claimedAuthorsLower)
		if totalClaimed > 0 {
			score += (float64(authorMatches) / float64(totalClaimed)) * 0.5
			if authorMatches > 0 && authorMatches < totalClaimed {
				hasPartialAuthorMatch = true
			}
		}
		if journalMatch {
			score += 0.3
		}
		if yearDiff == 0 {
			score += 0.2
		} else if yearDiff <= 1 {
			score += 0.15
		} else if yearDiff <= 2 {
			score += 0.1
		}

		if score > bestScore {
			bestScore = score
			candidate = item
		}
	}

	// Decision threshold
	// If 2+ authors were claimed, but only 1 was found and no paper with both exists in that journal/year
	if len(claimedAuthorsLower) >= 2 && hasPartialAuthorMatch && bestScore < 0.75 {
		return StatusChimera, candidate, bestScore, fmt.Sprintf("Chimère bibliographique détectée : les auteurs présumés (%s) n'ont pas co-publié ce travail dans %s.", strings.Join(pc.Authors, " & "), pc.Journal)
	}

	if bestScore >= 0.7 && candidate != nil && candidate.DOI != "" {
		return StatusValidated, candidate, bestScore, fmt.Sprintf("Publication authentifiée sur Crossref (DOI: %s).", candidate.DOI)
	}

	if bestScore >= 0.5 && candidate != nil {
		// Possible anachronism or inexact title/year but identifiable paper
		return StatusCorrected, candidate, bestScore, fmt.Sprintf("Publication approchante identifiée (DOI: %s), métadonnées à réaligner.", candidate.DOI)
	}

	return StatusNotFound, nil, bestScore, "Aucune concordance scientifique suffisante trouvée sur Crossref."
}

// ExtractItemAuthors returns author family names from a CrossrefItem with original casing.
func ExtractItemAuthors(item *CrossrefItem) []string {
	if item == nil {
		return nil
	}
	var authors []string
	for _, a := range item.Author {
		if a.Family != "" {
			authors = append(authors, a.Family)
		}
	}
	return authors
}

func extractItemAuthors(item *CrossrefItem) []string {
	var authors []string
	for _, a := range item.Author {
		if a.Family != "" {
			authors = append(authors, strings.ToLower(a.Family))
		}
	}
	return authors
}

func extractItemJournal(item *CrossrefItem) string {
	if len(item.ContainerTitle) > 0 {
		return item.ContainerTitle[0]
	}
	return ""
}

func extractItemYear(item *CrossrefItem) int {
	if len(item.Issued.DateParts) > 0 && len(item.Issued.DateParts[0]) > 0 {
		return item.Issued.DateParts[0][0]
	}
	return 0
}
