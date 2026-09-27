package memory

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryIndex provides O(1) lookups on LTM entries by various dimensions.
// It is rebuilt from scratch on Load() and maintained incrementally on Store/Delete.
type MemoryIndex struct {
	mu         sync.RWMutex
	byDate     map[string][]int // "2026-05-20" → [idx1, idx2, ...]
	byTag      map[string][]int // "pixel" → [idx1, idx2, ...]
	byCategory map[string][]int // "Personal" → [idx1, idx2, ...]
	bySource   map[string][]int // "conversation" → [idx1, idx2, ...]
}

// NewMemoryIndex creates a new empty index.
func NewMemoryIndex() *MemoryIndex {
	return &MemoryIndex{
		byDate:     make(map[string][]int),
		byTag:      make(map[string][]int),
		byCategory: make(map[string][]int),
		bySource:   make(map[string][]int),
	}
}

// Rebuild reconstructs all index maps from the full entries slice.
func (idx *MemoryIndex) Rebuild(entries []MemoryEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.byDate = make(map[string][]int)
	idx.byTag = make(map[string][]int)
	idx.byCategory = make(map[string][]int)
	idx.bySource = make(map[string][]int)

	for i, entry := range entries {
		idx.indexEntry(i, entry)
	}
}

// AddEntry adds a single entry to the index (used for incremental updates).
func (idx *MemoryIndex) AddEntry(position int, entry MemoryEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.indexEntry(position, entry)
}

// indexEntry adds a single entry to all index dimensions (caller must hold lock).
func (idx *MemoryIndex) indexEntry(position int, entry MemoryEntry) {
	// Date index
	dateKey := entry.Timestamp.Format("2006-01-02")
	idx.byDate[dateKey] = append(idx.byDate[dateKey], position)

	// Tag index
	for _, tag := range entry.Tags {
		tagLower := strings.ToLower(strings.TrimSpace(tag))
		if tagLower != "" {
			idx.byTag[tagLower] = append(idx.byTag[tagLower], position)
			tagFolded := FoldString(tagLower)
			if tagFolded != tagLower {
				idx.byTag[tagFolded] = append(idx.byTag[tagFolded], position)
			}
		}
	}

	// Category index
	if entry.Category != "" {
		idx.byCategory[entry.Category] = append(idx.byCategory[entry.Category], position)
	}

	// Source index
	if entry.Source != "" {
		idx.bySource[entry.Source] = append(idx.bySource[entry.Source], position)
	}
}

// GetByDate returns indices of entries for a specific date (format: "2006-01-02").
func (idx *MemoryIndex) GetByDate(date string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	result := make([]int, len(idx.byDate[date]))
	copy(result, idx.byDate[date])
	return result
}

// GetByDateRange returns indices of entries within a time range [from, to).
func (idx *MemoryIndex) GetByDateRange(from, to time.Time) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	var result []int
	for dateStr, indices := range idx.byDate {
		date, err := time.ParseInLocation("2006-01-02", dateStr, from.Location())
		if err != nil {
			continue
		}
		// Check if this date falls within [from_date, to_date)
		dayStart := date
		dayEnd := date.AddDate(0, 0, 1)
		if dayEnd.After(from) && dayStart.Before(to) {
			result = append(result, indices...)
		}
	}
	return result
}

// GetByTag returns indices of entries with a specific tag.
func (idx *MemoryIndex) GetByTag(tag string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	key := strings.ToLower(strings.TrimSpace(tag))
	result := make([]int, len(idx.byTag[key]))
	copy(result, idx.byTag[key])
	return result
}

// GetByCategory returns indices of entries with a specific category.
func (idx *MemoryIndex) GetByCategory(category string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	result := make([]int, len(idx.byCategory[category]))
	copy(result, idx.byCategory[category])
	return result
}

// GetBySource returns indices of entries with a specific source.
func (idx *MemoryIndex) GetBySource(source string) []int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	result := make([]int, len(idx.bySource[source]))
	copy(result, idx.bySource[source])
	return result
}

// Intersect returns the intersection of two sorted index slices.
func Intersect(a, b []int) []int {
	sort.Ints(a)
	sort.Ints(b)

	var result []int
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			result = append(result, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return result
}
