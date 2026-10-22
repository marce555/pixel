package citations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseCitations(t *testing.T) {
	htmlSample := `<article>
<h2>Introduction</h2>
<p>Texte explicatif...</p>
<div class="references-section">
<h3>Références et Sources</h3>
<table class="table">
<thead><tr><th>Référence</th><th>Revue</th><th>Type</th><th>Description</th></tr></thead>
<tbody>
<tr><td>Yamanaka, S. et Thomson, J. H. (2014)</td><td>Cell</td><td>Article de recherche</td><td>Protocoles de reprogrammation cellulaire</td></tr>
<tr><td>Ertesvag, H. et al. (2016)</td><td>Nature</td><td>Article de recherche</td><td>Hérédité épigénétique artificielle</td></tr>
<tr><td>GIEC (2021)</td><td>Rapport officiel</td><td>Consensus</td><td>Sixième rapport d'évaluation sur le climat</td></tr>
</tbody>
</table>
</div>
</article>`

	citations := ParseCitations(htmlSample)
	if len(citations) != 3 {
		t.Fatalf("expected 3 citations parsed, got %d", len(citations))
	}

	// Citation 1
	c1 := citations[0]
	if c1.Year != 2014 {
		t.Errorf("expected year 2014, got %d", c1.Year)
	}
	if len(c1.Authors) != 2 {
		t.Errorf("expected 2 authors (Yamanaka, Thomson), got %v", c1.Authors)
	}
	if c1.Journal != "Cell" {
		t.Errorf("expected journal Cell, got %s", c1.Journal)
	}

	// Citation 2
	c2 := citations[1]
	if c2.Year != 2016 {
		t.Errorf("expected year 2016, got %d", c2.Year)
	}
	if len(c2.Authors) != 1 || c2.Authors[0] != "Ertesvag" {
		t.Errorf("expected author Ertesvag, got %v", c2.Authors)
	}

	// Citation 3
	c3 := citations[2]
	if c3.Year != 2021 {
		t.Errorf("expected year 2021, got %d", c3.Year)
	}

	// Test prefixed titles with em-dash and concept labels
	prefixedTable := `<table class="table">
<thead><tr><th>Référence</th><th>Type</th><th>Description</th></tr></thead>
<tbody>
<tr><td>Théorème de Non-Clonage — Wootters, W. K., & Zurek, W. H. (1982). "A single quantum cannot be cloned". Physical Review D</td><td>Étude</td><td>Théorème fondamental</td></tr>
<tr><td>Inégalités de Bell — Bell, J. S. (1964). "On the Einstein Podolsky Rosen paradox". Physics</td><td>Étude</td><td>Non-localité</td></tr>
</tbody>
</table>`
	citations2 := ParseCitations(prefixedTable)
	if len(citations2) != 2 {
		t.Fatalf("expected 2 citations parsed from prefixedTable, got %d", len(citations2))
	}
	if len(citations2[0].Authors) != 2 || citations2[0].Authors[0] != "Wootters" || citations2[0].Authors[1] != "Zurek" {
		t.Errorf("expected authors [Wootters, Zurek], got %v", citations2[0].Authors)
	}
	if citations2[0].Year != 1982 {
		t.Errorf("expected year 1982, got %d", citations2[0].Year)
	}
	if len(citations2[1].Authors) != 1 || citations2[1].Authors[0] != "Bell" {
		t.Errorf("expected single author [Bell], got %v", citations2[1].Authors)
	}
	if citations2[1].Year != 1964 {
		t.Errorf("expected year 1964, got %d", citations2[1].Year)
	}
}

func TestCrossrefClientMock(t *testing.T) {
	userAgentReceived := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userAgentReceived = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")

		resp := crossrefResponse{
			Status: "ok",
		}
		resp.Message.TotalResults = 1

		item := CrossrefItem{
			Title:          []string{"Induction of Pluripotent Stem Cells"},
			DOI:            "10.1016/j.cell.2006.07.024",
			ContainerTitle: []string{"Cell"},
			Score:          95.0,
		}
		item.Issued.DateParts = [][]int{{2006, 8, 25}}
		item.Author = []struct {
			Given  string `json:"given"`
			Family string `json:"family"`
		}{
			{Given: "Kazutoshi", Family: "Takahashi"},
			{Given: "Shinya", Family: "Yamanaka"},
		}
		resp.Message.Items = []CrossrefItem{item}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewCrossrefClient("test@appliyou.fr")
	client.SetBaseURL(server.URL)

	pc := ParsedCitation{
		Authors: []string{"Yamanaka"},
		Year:    2006,
		Journal: "Cell",
		Title:   "Pluripotent Stem Cells",
	}

	items, err := client.QueryBibliographic(context.Background(), pc)
	if err != nil {
		t.Fatalf("QueryBibliographic failed: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].DOI != "10.1016/j.cell.2006.07.024" {
		t.Errorf("expected DOI 10.1016/j.cell.2006.07.024, got %s", items[0].DOI)
	}
	if !strings.Contains(userAgentReceived, "mailto:test@appliyou.fr") {
		t.Errorf("expected polite User-Agent with mailto, got %q", userAgentReceived)
	}

	// Test EvaluateMatch
	status, best, conf, _ := EvaluateMatch(pc, items)
	if status != StatusValidated {
		t.Errorf("expected StatusValidated, got %s", status)
	}
	if conf < 0.7 {
		t.Errorf("expected high confidence, got %f", conf)
	}
	if best == nil || best.DOI != "10.1016/j.cell.2006.07.024" {
		t.Errorf("expected best item DOI match")
	}
}

func TestPubMedClientRateLimitAndMock(t *testing.T) {
	callTimes := []time.Time{}

	searchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callTimes = append(callTimes, time.Now())
		w.Header().Set("Content-Type", "application/json")
		resp := pubmedSearchResponse{}
		resp.ESearchResult.Count = "1"
		resp.ESearchResult.IDList = []string{"16904174"}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer searchServer.Close()

	sumServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callTimes = append(callTimes, time.Now())
		w.Header().Set("Content-Type", "application/json")

		doc := pubmedDocSum{
			UID:     "16904174",
			Title:   "Induction of pluripotent stem cells by defined factors",
			PubDate: "2006 Aug",
			Source:  "Cell",
		}
		doc.Authors = []struct {
			Name string `json:"name"`
		}{
			{Name: "Takahashi K"},
			{Name: "Yamanaka S"},
		}
		doc.ArticleIDs = []struct {
			IDType string `json:"idtype"`
			Value  string `json:"value"`
		}{
			{IDType: "pubmed", Value: "16904174"},
			{IDType: "doi", Value: "10.1016/j.cell.2006.07.024"},
		}

		rawDoc, _ := json.Marshal(doc)
		out := map[string]interface{}{
			"result": map[string]json.RawMessage{
				"16904174": rawDoc,
			},
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer sumServer.Close()

	client := NewPubMedClient("")
	client.SetEndpoints(searchServer.URL, sumServer.URL)

	pc := ParsedCitation{
		Authors: []string{"Yamanaka"},
		Year:    2006,
		Journal: "Cell",
	}

	articles, count, err := client.Search(context.Background(), pc)
	if err != nil {
		t.Fatalf("PubMed Search failed: %v", err)
	}
	if count != 1 || len(articles) != 1 {
		t.Fatalf("expected 1 article, got %d", len(articles))
	}
	if articles[0].DOI != "10.1016/j.cell.2006.07.024" {
		t.Errorf("expected DOI 10.1016/j.cell.2006.07.024, got %s", articles[0].DOI)
	}
}

func TestValidatorSanitizeAndEnrich(t *testing.T) {
	tmpLog, err := os.CreateTemp("", "citations_fraud_*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpLog.Name())
	tmpLog.Close()

	// Crossref mock returning real Caesar 2018 Nature paper
	crServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := crossrefResponse{Status: "ok"}

		item := CrossrefItem{
			Title:          []string{"Observed fingerprint of a weakening Atlantic Ocean overturning circulation"},
			DOI:            "10.1038/s41586-018-0006-5",
			ContainerTitle: []string{"Nature"},
			Score:          90.0,
		}
		item.Issued.DateParts = [][]int{{2018, 4, 12}}
		item.Author = []struct {
			Given  string `json:"given"`
			Family string `json:"family"`
		}{
			{Given: "L.", Family: "Caesar"},
			{Given: "S.", Family: "Rahmstorf"},
		}
		resp.Message.Items = []CrossrefItem{item}
		resp.Message.TotalResults = 1
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer crServer.Close()

	// PubMed mock returning 0 results
	pmSearchServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := pubmedSearchResponse{}
		resp.ESearchResult.Count = "0"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer pmSearchServer.Close()

	crClient := NewCrossrefClient("test@appliyou.fr")
	crClient.SetBaseURL(crServer.URL)
	pmClient := NewPubMedClient("")
	pmClient.SetEndpoints(pmSearchServer.URL, pmSearchServer.URL)

	validator := NewValidatorWithClients(crClient, pmClient, tmpLog.Name())

	htmlArticle := `<article>
<table class="table">
<thead><tr><th>Référence</th><th>Revue</th><th>Type</th><th>Description</th></tr></thead>
<tbody>
<tr><td>Caesar, L. et al. (2018)</td><td>Nature</td><td>Article de recherche</td><td>Ralentissement de la circulation AMOC.</td></tr>
<tr><td>Yamanaka, S. et Thomson, J. H. (2014)</td><td>Cell</td><td>Article de recherche</td><td>Protocoles de reprogrammation cellulaire</td></tr>
<tr><td>GIEC (2021)</td><td>Rapport officiel</td><td>Consensus</td><td>Sixième rapport d'évaluation</td></tr>
<tr><td>FakeAuthor, X. (2023)</td><td>Science</td><td>Base de données technique</td><td>Étude inventée introuvable</td></tr>
</tbody>
</table>
</article>`

	cleaned, rep, err := validator.ValidateAndSanitize(context.Background(), htmlArticle, nil)
	if err != nil {
		t.Fatalf("ValidateAndSanitize failed: %v", err)
	}
	for i, r := range rep.Results {
		t.Logf("Result %d: Status=%s Reason=%s Authors=%v Original=%q", i, r.Status, r.Reason, r.Citation.Authors, r.Citation.Original)
	}

	if rep.TotalScanned != 4 {
		t.Errorf("expected 4 scanned, got %d", rep.TotalScanned)
	}
	if rep.Validated != 1 {
		t.Errorf("expected 1 validated, got %d", rep.Validated)
	}
	if rep.InjectedDOIs != 1 {
		t.Errorf("expected 1 injected DOI, got %d", rep.InjectedDOIs)
	}
	if rep.Institutional != 1 {
		t.Errorf("expected 1 institutional, got %d", rep.Institutional)
	}
	if rep.ChimerasRejected != 1 {
		t.Errorf("expected 1 chimera rejected, got %d", rep.ChimerasRejected)
	}
	if rep.Removed != 1 {
		t.Errorf("expected 1 fake reference removed, got %d", rep.Removed)
	}

	// Verify DOI injected for Caesar
	if !strings.Contains(cleaned, `https://doi.org/10.1038/s41586-018-0006-5`) {
		t.Errorf("expected Caesar row to have injected DOI link, got:\n%s", cleaned)
	}

	// Verify Yamanaka & Thomson chimera was realigned
	if strings.Contains(cleaned, "Yamanaka, S. et Thomson, J. H. (2014)") {
		t.Errorf("expected Yamanaka & Thomson 2014 chimera to be removed or realigned")
	}
	if !strings.Contains(cleaned, "Yamanaka, S. et al. (2006)") {
		t.Errorf("expected realigned Yamanaka 2006 paper in result")
	}

	// Verify fake reference was removed
	if strings.Contains(cleaned, "FakeAuthor") {
		t.Errorf("expected FakeAuthor row to be completely removed")
	}

	// Verify fraud log file was written
	logContent, errRead := os.ReadFile(tmpLog.Name())
	if errRead != nil || len(logContent) == 0 {
		t.Errorf("expected fraud log to be written")
	}
}

func TestValidateAndSanitizeReplacesDeadDOI(t *testing.T) {
	// Mock HTTP server for DOI checks
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "1244440") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "1225829") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	crClient := NewCrossrefClient("test@appliyou.fr")
	crClient.SetBaseURL(server.URL)
	pmClient := NewPubMedClient("")
	pmClient.SetEndpoints(server.URL+"/search", server.URL+"/summary")

	validator := NewValidatorWithClients(crClient, pmClient, "test_fraud.log")
	defer os.Remove("test_fraud.log")
	validator.SetHTTPClient(server.Client())

	htmlArticle := `<table class="table">
<tbody>
<tr><td>Doudna, J. A. et Charpentier, E. (2014)</td><td>Science</td><td>Article de recherche</td><td>Création de la méthode CRISPR-Cas9 pour l'édition génomique. <a href="https://doi.org/10.1126/science.1244440" target="_blank">DOI: 10.1126/science.1244440</a></td></tr>
</tbody>
</table>`

	cleaned, rep, err := validator.ValidateAndSanitize(context.Background(), htmlArticle, nil)
	if err != nil {
		t.Fatalf("ValidateAndSanitize failed: %v", err)
	}

	for i, r := range rep.Results {
		t.Logf("Result %d: Status=%s Reason=%s Authors=%v Orig=%q", i, r.Status, r.Reason, r.Citation.Authors, r.Citation.Original)
	}

	if rep.Corrected == 0 && rep.InjectedDOIs == 0 {
		t.Errorf("expected correction of dead DOI")
	}

	if strings.Contains(cleaned, "1244440") {
		t.Errorf("expected dead DOI 1244440 to be eliminated from output, got:\n%s", cleaned)
	}

	if !strings.Contains(cleaned, "10.1126/science.1225829") {
		t.Errorf("expected live seminal DOI 10.1126/science.1225829 to be injected, got:\n%s", cleaned)
	}
}

func TestValidateAndSanitizeRejectsAuthorMismatchedDOI(t *testing.T) {
	// Server returns Delaware corporate law paper for DOI 10.1038/nbt.3529
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "nbt.3529") {
			resp := crossrefSingleResponse{
				Status: "ok",
			}
			resp.Message.DOI = "10.1038/nbt.3529"
			resp.Message.Title = []string{"Ignore the Delaware director-compensation cases at your peril"}
			resp.Message.ContainerTitle = []string{"Nature Biotechnology"}
			resp.Message.Author = []struct {
				Given  string `json:"given"`
				Family string `json:"family"`
			}{
				{Given: "David", Family: "Lilienfeld"},
				{Given: "Norman", Family: "Polovoy"},
			}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	crClient := NewCrossrefClient("test@appliyou.fr")
	crClient.SetBaseURL(server.URL)
	pmClient := NewPubMedClient("")
	pmClient.SetEndpoints(server.URL+"/search", server.URL+"/summary")

	validator := NewValidatorWithClients(crClient, pmClient, "test_fraud.log")
	defer os.Remove("test_fraud.log")
	validator.SetHTTPClient(server.Client())

	htmlArticle := `<table class="table">
<tbody>
<tr><td>Stoica, M. et al. (2016)</td><td>Nature Biotechnology</td><td>Article de recherche</td><td>Mémoire synthétique <a href="https://doi.org/10.1038/nbt.3529">DOI: 10.1038/nbt.3529</a></td></tr>
</tbody>
</table>`

	cleaned, rep, err := validator.ValidateAndSanitize(context.Background(), htmlArticle, nil)
	if err != nil {
		t.Fatalf("ValidateAndSanitize failed: %v", err)
	}

	for i, r := range rep.Results {
		t.Logf("Result %d: Status=%s Reason=%s Authors=%v", i, r.Status, r.Reason, r.Citation.Authors)
	}

	// Delaware corporate law DOI must NOT be accepted for Stoica
	if strings.Contains(cleaned, "10.1038/nbt.3529") {
		t.Errorf("expected mismatched DOI 10.1038/nbt.3529 to be rejected, got:\n%s", cleaned)
	}

	// Must be realigned to Bintu et al. Science 2016
	if !strings.Contains(cleaned, "10.1126/science.aab2956") {
		t.Errorf("expected realignment to Bintu et al. (Science 2016), got:\n%s", cleaned)
	}
}

func TestVerifyCandidatePublication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "10.1002/adfm.200701216") {
			resp := crossrefSingleResponse{
				Status: "ok",
			}
			resp.Message.DOI = "10.1002/adfm.200701216"
			resp.Message.Title = []string{"Eutectic Gallium‐Indium (EGaIn): A Liquid Metal Alloy"}
			resp.Message.ContainerTitle = []string{"Advanced Functional Materials"}
			resp.Message.Author = []struct {
				Given  string `json:"given"`
				Family string `json:"family"`
			}{
				{Given: "Michael", Family: "Dickey"},
				{Given: "George", Family: "Whitesides"},
			}
			resp.Message.Issued.DateParts = [][]int{{2008, 4, 15}}
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	crClient := NewCrossrefClient("test@appliyou.fr")
	crClient.SetBaseURL(server.URL)
	pmClient := NewPubMedClient("")
	pmClient.SetEndpoints(server.URL+"/search", server.URL+"/summary")

	validator := NewValidatorWithClients(crClient, pmClient, "test_candidate.log")
	defer os.Remove("test_candidate.log")
	validator.SetHTTPClient(server.Client())

	pcValid := ParsedCitation{
		Authors: []string{"Dickey", "Whitesides"},
		Year:    2008,
		Title:   "Eutectic Gallium-Indium (EGaIn)",
		Journal: "Advanced Functional Materials",
		DOI:     "10.1002/adfm.200701216",
	}

	cand, err := validator.VerifyCandidatePublication(context.Background(), pcValid)
	if err != nil {
		t.Fatalf("expected valid candidate verification, got err: %v", err)
	}
	if cand.DOI != "10.1002/adfm.200701216" {
		t.Errorf("expected DOI 10.1002/adfm.200701216, got %s", cand.DOI)
	}
	if len(cand.Authors) != 2 || !strings.EqualFold(cand.Authors[0], "Dickey") {
		t.Errorf("expected authors [Dickey Whitesides], got %v", cand.Authors)
	}

	// Mismatched authors must fail
	pcMismatched := ParsedCitation{
		Authors: []string{"Smith", "Johnson"},
		DOI:     "10.1002/adfm.200701216",
	}
	_, errMismatch := validator.VerifyCandidatePublication(context.Background(), pcMismatched)
	if errMismatch == nil {
		t.Errorf("expected error for author mismatch, got nil")
	}
}

