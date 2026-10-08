package citations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultPubMedSearchURL  = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi"
	defaultPubMedSummaryURL = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esummary.fcgi"
)

// PubMedArticle represents article metadata retrieved from PubMed esummary.
type PubMedArticle struct {
	UID        string   `json:"uid"`
	Title      string   `json:"title"`
	PubDate    string   `json:"pubdate"`
	Source     string   `json:"source"` // Journal name
	Authors    []string `json:"authors"`
	DOI        string   `json:"doi"`
	PMID       string   `json:"pmid"`
	ParsedYear int      `json:"parsed_year"`
}

type pubmedSearchResponse struct {
	Header struct {
		Type string `json:"type"`
	} `json:"header"`
	ESearchResult struct {
		Count  string   `json:"count"`
		IDList []string `json:"idlist"`
	} `json:"esearchresult"`
}

type pubmedSummaryResponse struct {
	Result map[string]json.RawMessage `json:"result"`
}

type pubmedDocSum struct {
	UID     string `json:"uid"`
	Title   string `json:"title"`
	PubDate string `json:"pubdate"`
	Source  string `json:"source"`
	Authors []struct {
		Name string `json:"name"`
	} `json:"authors"`
	ArticleIDs []struct {
		IDType string `json:"idtype"`
		Value  string `json:"value"`
	} `json:"articleids"`
}

// PubMedClient queries the NCBI Entrez / PubMed E-utilities with strict rate limiting.
type PubMedClient struct {
	httpClient  *http.Client
	searchURL   string
	summaryURL  string
	apiKey      string
	minInterval time.Duration
	mu          sync.Mutex
	lastCall    time.Time
}

// NewPubMedClient initializes a PubMedClient respecting the NCBI rate limit.
func NewPubMedClient(apiKey string) *PubMedClient {
	if apiKey == "" {
		apiKey = os.Getenv("NCBI_API_KEY")
	}

	// Rate limit: 10 req/s with API key, 3 req/s without key
	interval := 340 * time.Millisecond // 3 req/sec
	if apiKey != "" {
		interval = 100 * time.Millisecond // 10 req/sec
	}

	return &PubMedClient{
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		searchURL:   defaultPubMedSearchURL,
		summaryURL:  defaultPubMedSummaryURL,
		apiKey:      apiKey,
		minInterval: interval,
	}
}

// SetEndpoints overrides the search and summary endpoints (for testing with mocks).
func (c *PubMedClient) SetEndpoints(searchURL, summaryURL string) {
	c.searchURL = searchURL
	c.summaryURL = summaryURL
}

func (c *PubMedClient) waitRateLimit() {
	c.mu.Lock()
	defer c.mu.Unlock()

	elapsed := time.Since(c.lastCall)
	if elapsed < c.minInterval {
		time.Sleep(c.minInterval - elapsed)
	}
	c.lastCall = time.Now()
}

// Search queries PubMed for articles matching the citation parameters.
func (c *PubMedClient) Search(ctx context.Context, pc ParsedCitation) ([]PubMedArticle, int, error) {
	c.waitRateLimit()

	// 1. Build targeted query terms
	var terms []string
	for _, a := range pc.Authors {
		clean := strings.TrimSpace(a)
		if clean != "" {
			terms = append(terms, fmt.Sprintf("%s[Author]", clean))
		}
	}
	if pc.Year > 0 {
		terms = append(terms, fmt.Sprintf("%d[Date - Publication]", pc.Year))
	}
	if pc.Journal != "" {
		terms = append(terms, fmt.Sprintf("%s[Journal]", pc.Journal))
	}

	query := strings.Join(terms, " AND ")
	if query == "" {
		query = pc.RawText
	}

	searchParams := url.Values{}
	searchParams.Set("db", "pubmed")
	searchParams.Set("term", query)
	searchParams.Set("retmode", "json")
	searchParams.Set("retmax", "3")
	if c.apiKey != "" {
		searchParams.Set("api_key", c.apiKey)
	}

	reqURL := fmt.Sprintf("%s?%s", c.searchURL, searchParams.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "PixelBot/1.0 (PubMed Client)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("pubmed esearch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("pubmed HTTP %d: %s", resp.StatusCode, string(body))
	}

	var searchResp pubmedSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, 0, fmt.Errorf("failed to decode pubmed search response: %w", err)
	}

	totalCount, _ := strconv.Atoi(searchResp.ESearchResult.Count)
	if len(searchResp.ESearchResult.IDList) == 0 {
		return nil, totalCount, nil
	}

	// 2. Fetch summaries for returned IDs
	articles, err := c.FetchSummaries(ctx, searchResp.ESearchResult.IDList)
	if err != nil {
		return nil, totalCount, err
	}

	return articles, totalCount, nil
}

// FetchSummaries fetches document summaries for a list of PubMed IDs.
func (c *PubMedClient) FetchSummaries(ctx context.Context, uids []string) ([]PubMedArticle, error) {
	if len(uids) == 0 {
		return nil, nil
	}
	c.waitRateLimit()

	sumParams := url.Values{}
	sumParams.Set("db", "pubmed")
	sumParams.Set("id", strings.Join(uids, ","))
	sumParams.Set("retmode", "json")
	if c.apiKey != "" {
		sumParams.Set("api_key", c.apiKey)
	}

	reqURL := fmt.Sprintf("%s?%s", c.summaryURL, sumParams.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PixelBot/1.0 (PubMed Client)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pubmed esummary failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pubmed esummary HTTP %d", resp.StatusCode)
	}

	var rawSum pubmedSummaryResponse
	if err := json.NewDecoder(resp.Body).Decode(&rawSum); err != nil {
		return nil, fmt.Errorf("failed to decode pubmed summary response: %w", err)
	}

	var articles []PubMedArticle
	for _, uid := range uids {
		rawDoc, ok := rawSum.Result[uid]
		if !ok {
			continue
		}
		var doc pubmedDocSum
		if err := json.Unmarshal(rawDoc, &doc); err != nil {
			continue
		}

		art := PubMedArticle{
			UID:     uid,
			PMID:    uid,
			Title:   doc.Title,
			PubDate: doc.PubDate,
			Source:  doc.Source,
		}

		// Parse authors
		for _, a := range doc.Authors {
			art.Authors = append(art.Authors, a.Name)
		}

		// Parse year from PubDate (e.g. "2006 Aug 25")
		if ym := reYear.FindString(doc.PubDate); ym != "" {
			art.ParsedYear, _ = strconv.Atoi(ym)
		}

		// Parse DOI
		for _, aid := range doc.ArticleIDs {
			if aid.IDType == "doi" {
				art.DOI = aid.Value
				break
			}
		}

		articles = append(articles, art)
	}

	return articles, nil
}
