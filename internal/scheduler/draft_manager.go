package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type DraftStatus string

const (
	DraftStatusDraft      DraftStatus = "draft"
	DraftStatusInReview   DraftStatus = "in_review"
	DraftStatusValidated  DraftStatus = "validated"
	DraftStatusPublishing DraftStatus = "publishing"
	DraftStatusPublished  DraftStatus = "published"
	DraftStatusFailed     DraftStatus = "failed"
)

// ArticleDraft represents an article stored in the persistent temporary cache.
type ArticleDraft struct {
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	Topic           string      `json:"topic"`
	Category        string      `json:"category"`
	Keywords        string      `json:"keywords"`
	RawContent      string      `json:"raw_content"`
	ReviewedContent string      `json:"reviewed_content,omitempty"`
	ReviewNotes     string      `json:"review_notes,omitempty"`
	Status          DraftStatus `json:"status"`
	ErrorLog        string      `json:"error_log,omitempty"`
	RetryCount      int         `json:"retry_count"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	PublishedAt     *time.Time  `json:"published_at,omitempty"`
}

// GetEffectiveContent returns the reviewed content if available, or the raw content otherwise.
func (d *ArticleDraft) GetEffectiveContent() string {
	if strings.TrimSpace(d.ReviewedContent) != "" {
		return d.ReviewedContent
	}
	return d.RawContent
}

// DraftManager manages persistent temporary files for article drafts.
type DraftManager struct {
	dir string
	mu  sync.RWMutex
}

// NewDraftManager initializes a DraftManager with a designated directory (default: "drafts").
func NewDraftManager(dir string) *DraftManager {
	if dir == "" {
		dir = "drafts"
	}
	_ = os.MkdirAll(dir, 0755)
	return &DraftManager{
		dir: dir,
	}
}

// GetDirectory returns the storage directory path.
func (m *DraftManager) GetDirectory() string {
	return m.dir
}

// CreateDraft initializes a new ArticleDraft, persists it to disk, and returns it.
func (m *DraftManager) CreateDraft(title, topic, category, keywords, rawContent string) (*ArticleDraft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	cleanID := fmt.Sprintf("draft_%s_%s", now.Format("20060102_150405"), uuid.New().String()[:8])

	if category == "" {
		category = "Technologies"
	}

	draft := &ArticleDraft{
		ID:         cleanID,
		Title:      title,
		Topic:      topic,
		Category:   category,
		Keywords:   keywords,
		RawContent: rawContent,
		Status:     DraftStatusDraft,
		RetryCount: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := m.saveToFile(draft); err != nil {
		return nil, fmt.Errorf("failed to save new draft to file: %w", err)
	}

	return draft, nil
}

// SaveDraft saves or updates an existing draft to disk.
func (m *DraftManager) SaveDraft(draft *ArticleDraft) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	draft.UpdatedAt = time.Now()
	return m.saveToFile(draft)
}

func (m *DraftManager) saveToFile(draft *ArticleDraft) error {
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return err
	}

	filePath := filepath.Join(m.dir, fmt.Sprintf("%s.json", draft.ID))
	data, err := json.MarshalIndent(draft, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically via temporary file
	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, filePath)
}

// GetDraft retrieves a draft by its unique ID.
func (m *DraftManager) GetDraft(id string) (*ArticleDraft, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cleanID := strings.TrimSuffix(id, ".json")
	filePath := filepath.Join(m.dir, fmt.Sprintf("%s.json", cleanID))
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("draft '%s' not found: %w", id, err)
	}

	var draft ArticleDraft
	if err := json.Unmarshal(data, &draft); err != nil {
		return nil, fmt.Errorf("corrupted draft '%s': %w", id, err)
	}

	return &draft, nil
}

// ListDrafts returns all drafts optionally filtered by status, sorted by CreatedAt descending.
func (m *DraftManager) ListDrafts(statuses ...DraftStatus) ([]*ArticleDraft, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	filterMap := make(map[DraftStatus]bool)
	for _, s := range statuses {
		filterMap[s] = true
	}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*ArticleDraft{}, nil
		}
		return nil, err
	}

	var drafts []*ArticleDraft
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		filePath := filepath.Join(m.dir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		var d ArticleDraft
		if err := json.Unmarshal(data, &d); err != nil {
			continue
		}

		if len(filterMap) > 0 && !filterMap[d.Status] {
			continue
		}

		drafts = append(drafts, &d)
	}

	sort.Slice(drafts, func(i, j int) bool {
		return drafts[i].CreatedAt.After(drafts[j].CreatedAt)
	})

	return drafts, nil
}

// UpdateDraftStatus updates the lifecycle status and error log of a draft.
func (m *DraftManager) UpdateDraftStatus(id string, status DraftStatus, errorLog string) error {
	draft, err := m.GetDraft(id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	draft.Status = status
	if errorLog != "" {
		draft.ErrorLog = errorLog
	}
	if status == DraftStatusPublished {
		now := time.Now()
		draft.PublishedAt = &now
	}
	draft.UpdatedAt = time.Now()

	return m.saveToFile(draft)
}

// UpdateReviewedContent updates the proofread content, review notes and sets status to validated.
func (m *DraftManager) UpdateReviewedContent(id string, reviewedTitle, reviewedKeywords, reviewedContent, reviewNotes string) error {
	draft, err := m.GetDraft(id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if reviewedTitle != "" {
		draft.Title = reviewedTitle
	}
	if reviewedKeywords != "" {
		draft.Keywords = reviewedKeywords
	}
	if reviewedContent != "" {
		draft.ReviewedContent = reviewedContent
	}
	draft.ReviewNotes = reviewNotes
	draft.Status = DraftStatusValidated
	draft.UpdatedAt = time.Now()

	return m.saveToFile(draft)
}

// IncrementRetryCount increments the retry counter for a draft upon retry.
func (m *DraftManager) IncrementRetryCount(id string) error {
	draft, err := m.GetDraft(id)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	draft.RetryCount++
	draft.UpdatedAt = time.Now()

	return m.saveToFile(draft)
}

// DeleteDraft removes a draft from disk.
func (m *DraftManager) DeleteDraft(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cleanID := strings.TrimSuffix(id, ".json")
	filePath := filepath.Join(m.dir, fmt.Sprintf("%s.json", cleanID))
	return os.Remove(filePath)
}

// FindLatestDraftByTitle searches for the latest draft matching the specified title.
func (m *DraftManager) FindLatestDraftByTitle(title string) (*ArticleDraft, error) {
	drafts, err := m.ListDrafts()
	if err != nil {
		return nil, err
	}
	cleanTarget := strings.ToLower(strings.TrimSpace(title))
	for _, d := range drafts {
		dTitle := strings.ToLower(strings.TrimSpace(d.Title))
		if dTitle == cleanTarget || strings.Contains(dTitle, cleanTarget) || strings.Contains(cleanTarget, dTitle) {
			return d, nil
		}
	}
	return nil, fmt.Errorf("no draft found matching title %q", title)
}

