package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/marce555/pixel/internal/llm"
)

type mockEventBroadcaster struct {
	messages []string
}

func (m *mockEventBroadcaster) Broadcast(message string) {
	m.messages = append(m.messages, message)
}

type mockSTMWriter struct {
	messages []llm.Message
}

func (m *mockSTMWriter) AddMessage(msg llm.Message) {
	m.messages = append(m.messages, msg)
}

type mockReviewer struct {
	reviewedTitle    string
	reviewedKeywords string
	reviewedContent  string
	reviewNotes      string
	callCount        int
}

func (m *mockReviewer) Review(ctx context.Context, draft *ArticleDraft) (string, string, string, string, error) {
	m.callCount++
	return m.reviewedTitle, m.reviewedKeywords, m.reviewedContent, m.reviewNotes, nil
}

func TestPublishArticleHandlerInvalidPayload(t *testing.T) {
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}
	handler := NewPublishArticleHandler(broadcaster, stm, nil, nil)

	ctx := context.Background()
	task := &Task{
		Payload: "invalid json",
	}

	err := handler(ctx, task)
	if err == nil {
		t.Fatal("expected handler to fail with invalid JSON payload")
	}
}

func TestPublishArticleHandlerDraftStagingAndReviewFailurePreservation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pixel_publish_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dm := NewDraftManager(tempDir)
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}

	reviewer := &mockReviewer{
		reviewedTitle:    "Titre Relecture Parfaite",
		reviewedKeywords: "cybersecurity cloud",
		reviewedContent:  "<p>Contenu validé avec tableau.</p><table><tr><td>OK</td></tr></table>",
		reviewNotes:      "Relecture validée avec succès",
	}

	// Environment missing PIXEL_PUBLISH_PASS so publication step will fail intentionally
	_ = os.Unsetenv("PIXEL_PUBLISH_PASS")

	handler := NewPublishArticleHandlerWithReviewer(broadcaster, stm, nil, nil, dm, reviewer)

	payload := ArticlePayload{
		Title:    "Article Test Sécurité",
		Topic:    "Sécurité Cloud",
		Category: "Technologies",
		Keywords: "security",
		Content:  strings.Repeat("<p>Contenu de test suffisamment long pour éviter la régénération automatique.</p>\n", 15),
	}
	payloadBytes, _ := json.Marshal(payload)

	task := &Task{
		Payload: string(payloadBytes),
	}

	ctx := context.Background()
	err = handler(ctx, task)
	if err == nil {
		t.Fatal("expected handler to fail due to missing PIXEL_PUBLISH_PASS")
	}

	// Verify that reviewer was called!
	if reviewer.callCount != 1 {
		t.Fatalf("expected reviewer to be called once, got %d", reviewer.callCount)
	}

	// Verify that draft was preserved in DraftManager with status failed!
	drafts, errList := dm.ListDrafts()
	if errList != nil {
		t.Fatalf("ListDrafts failed: %v", errList)
	}
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft saved, got %d", len(drafts))
	}

	savedDraft := drafts[0]
	if savedDraft.Status != DraftStatusFailed {
		t.Fatalf("expected draft status to be failed, got %s", savedDraft.Status)
	}
	if savedDraft.Title != "Titre Relecture Parfaite" {
		t.Fatalf("expected reviewed title 'Titre Relecture Parfaite', got '%s'", savedDraft.Title)
	}
	if !strings.Contains(savedDraft.GetEffectiveContent(), "Contenu validé avec tableau.") {
		t.Fatalf("expected draft effective content to contain reviewed content")
	}
	if !strings.Contains(savedDraft.ErrorLog, "PIXEL_PUBLISH_PASS") {
		t.Fatalf("expected error log to mention PIXEL_PUBLISH_PASS, got: %s", savedDraft.ErrorLog)
	}
	if len(broadcaster.messages) == 0 {
		t.Fatalf("expected broadcaster notification upon failure")
	}
	if !strings.Contains(broadcaster.messages[0], savedDraft.ID) {
		t.Fatalf("expected broadcaster message to reference draft ID: %s", broadcaster.messages[0])
	}
}

func TestPublishArticleHandlerResumeFromDraftID(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pixel_publish_resume_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dm := NewDraftManager(tempDir)
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}

	// Pre-create an already validated draft
	draft, err := dm.CreateDraft("Article Déjà Rédigé", "Sujet", "Technologies", "ai", "<p>Contenu existant...</p>")
	if err != nil {
		t.Fatalf("CreateDraft failed: %v", err)
	}
	_ = dm.UpdateReviewedContent(draft.ID, "Article Déjà Validé", "ai-vision", "<p>Contenu validé...</p>", "Notes existantes")

	reviewer := &mockReviewer{}
	handler := NewPublishArticleHandlerWithReviewer(broadcaster, stm, nil, nil, dm, reviewer)

	resumePayload := ArticlePayload{
		DraftID: draft.ID,
	}
	payloadBytes, _ := json.Marshal(resumePayload)

	task := &Task{
		Payload: string(payloadBytes),
	}

	_ = os.Unsetenv("PIXEL_PUBLISH_PASS")
	ctx := context.Background()
	_ = handler(ctx, task)

	// Verify that reviewer was NOT called again because draft was already validated!
	if reviewer.callCount != 0 {
		t.Fatalf("expected reviewer not to be called again on already validated draft, got %d", reviewer.callCount)
	}

	// Verify retry count incremented
	reloaded, _ := dm.GetDraft(draft.ID)
	if reloaded.RetryCount != 1 {
		t.Fatalf("expected retry count 1, got %d", reloaded.RetryCount)
	}
}

type mockFactChecker struct {
	report           *FactCheckReport
	correctedContent string
	err              error
	callCount        int
}

func (m *mockFactChecker) Check(ctx context.Context, title, content string, logf func(string)) (*FactCheckReport, string, error) {
	m.callCount++
	return m.report, m.correctedContent, m.err
}

func TestPublishArticleHandlerFactCheckAdjustmentReported(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pixel_publish_fc_adj_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dm := NewDraftManager(tempDir)
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}
	reviewer := &mockReviewer{
		reviewedTitle:    "Article vérifié",
		reviewedKeywords: "science",
		reviewedContent:  "<p>Article brut avec des affirmations à vérifier.</p>",
	}

	fc := &mockFactChecker{
		report: &FactCheckReport{
			Verdict:     FactCheckCorrected,
			Model:       "gemini-3.6-flash",
			Confirmed:   2,
			Imprecise:   1,
			Corrections: 1,
			Claims: []ClaimVerdict{
				{
					Claim:      "Affirmation inexacte corrigée",
					Verdict:    ClaimImprecise,
					Correction: "Affirmation rectifiée selon la source.",
					SourceURL:  "https://example.com/source",
					Applied:    true,
				},
			},
		},
		correctedContent: "<p>Article rectifié selon la source.</p>",
	}

	_ = os.Unsetenv("PIXEL_PUBLISH_PASS")
	handler := NewPublishArticleHandlerWithFactChecker(broadcaster, stm, nil, nil, dm, reviewer, fc)

	payload := ArticlePayload{
		Title:   "Article vérifié",
		Topic:   "Science",
		Content: strings.Repeat("<p>Contenu suffisant pour ne pas déclencher la régénération.</p>\n", 15),
	}
	payloadBytes, _ := json.Marshal(payload)
	task := &Task{Payload: string(payloadBytes)}

	_ = handler(context.Background(), task)

	if fc.callCount != 1 {
		t.Fatalf("expected factChecker to be called once, got %d", fc.callCount)
	}

	// Verify that the draft has the fact checked content and report stored
	drafts, _ := dm.ListDrafts()
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	draft := drafts[0]
	if draft.FactCheck == nil {
		t.Fatalf("expected draft.FactCheck to be stored")
	}
	if !draft.FactCheck.WasAdjusted() {
		t.Errorf("expected WasAdjusted() == true")
	}
	if draft.GetEffectiveContent() != fc.correctedContent {
		t.Errorf("expected GetEffectiveContent to return corrected content")
	}

	// Check that broadcast failure message includes fact-check summary
	if len(broadcaster.messages) == 0 {
		t.Fatalf("expected broadcast message")
	}
	lastMsg := broadcaster.messages[len(broadcaster.messages)-1]
	if !strings.Contains(lastMsg, "a dû ajuster mes propos") {
		t.Errorf("expected broadcast to report that statements were adjusted, got:\n%s", lastMsg)
	}
	if !strings.Contains(lastMsg, "gemini-3.6-flash") {
		t.Errorf("expected broadcast to report independent model, got:\n%s", lastMsg)
	}
}

func TestPublishArticleHandlerFactCheckRejectionBlocksPublication(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pixel_publish_fc_rej_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dm := NewDraftManager(tempDir)
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}
	reviewer := &mockReviewer{
		reviewedTitle:    "Article Trompeur",
		reviewedKeywords: "fake",
		reviewedContent:  "<p>Article plein d'erreurs.</p>",
	}

	fc := &mockFactChecker{
		report: &FactCheckReport{
			Verdict: FactCheckRejected,
			Model:   "gemini-3.6-flash",
			Summary: "Trop d'affirmations fausses (50% >= 40%)",
			False:   2,
			Claims: []ClaimVerdict{
				{Claim: "Faux 1", Verdict: ClaimFalse},
				{Claim: "Faux 2", Verdict: ClaimFalse},
			},
		},
	}

	handler := NewPublishArticleHandlerWithFactChecker(broadcaster, stm, nil, nil, dm, reviewer, fc)

	payload := ArticlePayload{
		Title:   "Article Trompeur",
		Topic:   "Fake",
		Content: strings.Repeat("<p>Contenu long...</p>\n", 15),
	}
	payloadBytes, _ := json.Marshal(payload)
	task := &Task{Payload: string(payloadBytes)}

	err = handler(context.Background(), task)
	if err == nil {
		t.Fatal("expected handler to fail and block publication when factChecker rejects")
	}
	if !strings.Contains(err.Error(), "bloquée par le vérificateur factuel") {
		t.Fatalf("unexpected error message: %v", err)
	}

	drafts, _ := dm.ListDrafts()
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft saved, got %d", len(drafts))
	}
	if drafts[0].Status != DraftStatusRejectedFactCheck {
		t.Errorf("expected draft status %s, got %s", DraftStatusRejectedFactCheck, drafts[0].Status)
	}

	if len(broadcaster.messages) == 0 {
		t.Fatalf("expected broadcast message")
	}
	if !strings.Contains(broadcaster.messages[0], "Publication bloquée par l'Agent Vérificateur") {
		t.Errorf("expected blocking broadcast, got: %s", broadcaster.messages[0])
	}
}

type mockBiblioAuditor struct {
	auditedContent string
	report         *BibliographicAuditReport
	called         bool
}

func (m *mockBiblioAuditor) Audit(ctx context.Context, title, content string, logf func(string)) (string, *BibliographicAuditReport, error) {
	m.called = true
	return m.auditedContent, m.report, nil
}

func TestPublishArticleHandlerBibliographicAuditExecuted(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "drafts_biblio_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dm := NewDraftManager(tmpDir)
	broadcaster := &mockEventBroadcaster{}
	stm := &mockSTMWriter{}
	reviewer := &mockReviewer{reviewedTitle: "Titre Valide", reviewedContent: "<article class=\"blog-article\"><p>Contenu validé...</p></article>"}
	fc := &mockFactChecker{
		report: &FactCheckReport{
			Verdict:   FactCheckValidated,
			Summary:   "Tout est vérifié",
			Confirmed: 2,
		},
	}
	biblio := &mockBiblioAuditor{
		auditedContent: "<article class=\"blog-article\"><p>Contenu avec correction affiliation...</p></article>",
		report: &BibliographicAuditReport{
			Conserved: 1,
			Corrected: 1,
			Actions:   []string{"[CORRIGÉ] Université de Londres → Université du Wisconsin à Madison"},
		},
	}

	handler := NewPublishArticleHandlerWithBibliographicAuditor(broadcaster, stm, nil, nil, dm, reviewer, fc, biblio)

	payload := ArticlePayload{
		Title:   "Article Avec Affiliation",
		Topic:   "Génétique",
		Content: strings.Repeat("<p>Contenu long sur la génétique...</p>\n", 15),
	}
	payloadBytes, _ := json.Marshal(payload)
	task := &Task{Payload: string(payloadBytes)}

	_ = handler(context.Background(), task)

	if !biblio.called {
		t.Errorf("expected bibliographic auditor to be called in publishing pipeline")
	}

	drafts, _ := dm.ListDrafts()
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft saved, got %d", len(drafts))
	}
	if drafts[0].BiblioAudit == nil {
		t.Fatalf("expected draft to contain BiblioAudit report")
	}
	if drafts[0].BiblioAudit.Corrected != 1 {
		t.Errorf("expected 1 corrected in draft BiblioAudit, got %d", drafts[0].BiblioAudit.Corrected)
	}
}
