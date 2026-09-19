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
	if savedDraft.GetEffectiveContent() != reviewer.reviewedContent {
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
