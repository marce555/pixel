package scheduler

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDraftManagerLifecycle(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "pixel_drafts_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dm := NewDraftManager(tempDir)

	// 1. Create draft
	draft, err := dm.CreateDraft("Titre Test", "Sujet Test", "Technologies", "ai, cloud", "<p>Contenu brut</p>")
	if err != nil {
		t.Fatalf("CreateDraft failed: %v", err)
	}
	if draft.ID == "" {
		t.Fatal("expected non-empty draft ID")
	}
	if draft.Status != DraftStatusDraft {
		t.Fatalf("expected status draft, got %s", draft.Status)
	}

	// Verify file exists on disk
	expectedFile := filepath.Join(tempDir, draft.ID+".json")
	if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
		t.Fatalf("draft file does not exist: %s", expectedFile)
	}

	// 2. Get draft
	loaded, err := dm.GetDraft(draft.ID)
	if err != nil {
		t.Fatalf("GetDraft failed: %v", err)
	}
	if loaded.Title != "Titre Test" {
		t.Fatalf("expected title 'Titre Test', got '%s'", loaded.Title)
	}

	// 3. Update reviewed content
	reviewedContent := "<p>Contenu relu et corrigé</p><table><tr><td>Data</td></tr></table>"
	reviewNotes := "Validation OK: tableau présent"
	err = dm.UpdateReviewedContent(draft.ID, "Titre Optimisé", "ai-art, cloud-server", reviewedContent, reviewNotes)
	if err != nil {
		t.Fatalf("UpdateReviewedContent failed: %v", err)
	}

	loaded2, err := dm.GetDraft(draft.ID)
	if err != nil {
		t.Fatalf("GetDraft after review failed: %v", err)
	}
	if loaded2.Status != DraftStatusValidated {
		t.Fatalf("expected status %s, got %s", DraftStatusValidated, loaded2.Status)
	}
	if loaded2.Title != "Titre Optimisé" {
		t.Fatalf("expected updated title, got %s", loaded2.Title)
	}
	if loaded2.GetEffectiveContent() != reviewedContent {
		t.Fatalf("expected effective content to return reviewed content")
	}

	// 4. Update status to Failed with error log
	err = dm.UpdateDraftStatus(draft.ID, DraftStatusFailed, "Network timeout on page 2")
	if err != nil {
		t.Fatalf("UpdateDraftStatus failed: %v", err)
	}

	failedDrafts, err := dm.ListDrafts(DraftStatusFailed)
	if err != nil {
		t.Fatalf("ListDrafts failed: %v", err)
	}
	if len(failedDrafts) != 1 {
		t.Fatalf("expected 1 failed draft, got %d", len(failedDrafts))
	}
	if failedDrafts[0].ErrorLog != "Network timeout on page 2" {
		t.Fatalf("expected error log preserved, got %s", failedDrafts[0].ErrorLog)
	}

	// 5. Test increment retry count
	err = dm.IncrementRetryCount(draft.ID)
	if err != nil {
		t.Fatalf("IncrementRetryCount failed: %v", err)
	}
	reloaded, _ := dm.GetDraft(draft.ID)
	if reloaded.RetryCount != 1 {
		t.Fatalf("expected retry count 1, got %d", reloaded.RetryCount)
	}

	// 6. Delete draft
	err = dm.DeleteDraft(draft.ID)
	if err != nil {
		t.Fatalf("DeleteDraft failed: %v", err)
	}
	_, err = dm.GetDraft(draft.ID)
	if err == nil {
		t.Fatal("expected error after deleting draft, got nil")
	}
}
