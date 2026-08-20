package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/skills"
)

// DiagnosticEntry represents a single diagnostic signal sent by Pixel.
type DiagnosticEntry struct {
	Timestamp   string `json:"timestamp"`
	Category    string `json:"category"` // "missing_skill", "skill_error", "assistance_request"
	Description string `json:"description"`
	Query       string `json:"query,omitempty"`
	Status      string `json:"status"` // "reported", "building_skill", "resolved", "failed"
	ResolvedBy  string `json:"resolved_by,omitempty"`
}

// AntigravityBridge handles direct communication between Pixel's cognitive agents and the self-evolution engine.
type AntigravityBridge struct {
	mu           sync.Mutex
	logPath      string
	skillManager *skills.SkillManager
}

// NewAntigravityBridge initializes the bridge with a shared log path in ~/.gemini/antigravity/pixel_diagnostics.json.
func NewAntigravityBridge(sm *skills.SkillManager) *AntigravityBridge {
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".gemini", "antigravity")
	os.MkdirAll(logDir, 0755)

	return &AntigravityBridge{
		logPath:      filepath.Join(logDir, "pixel_diagnostics.json"),
		skillManager: sm,
	}
}

// ReportNeed logs an error or missing capability and triggers autonomous skill generation if needed.
func (b *AntigravityBridge) ReportNeed(ctx context.Context, category string, description string, query string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	fmt.Printf("[AntigravityBridge] Signal reçu : [%s] %s\n", category, description)

	entry := DiagnosticEntry{
		Timestamp:   time.Now().Format(time.RFC3339),
		Category:    category,
		Description: description,
		Query:       query,
		Status:      "reported",
	}

	// 1. Append diagnostic to pixel_diagnostics.json
	var entries []DiagnosticEntry
	if data, err := os.ReadFile(b.logPath); err == nil {
		json.Unmarshal(data, &entries)
	}
	entries = append(entries, entry)
	if data, err := json.MarshalIndent(entries, "", "  "); err == nil {
		os.WriteFile(b.logPath, data, 0644)
	}

	// 2. Trigger autonomous skill build if skillManager is available
	if b.skillManager != nil && (category == "missing_skill" || category == "skill_error") {
		fmt.Printf("[AntigravityBridge] Déclenchement de l'auto-construction de skill pour : %s\n", description)
		msg, skillName, err := b.skillManager.BuildSkill(ctx, description)
		if err == nil {
			entry.Status = "resolved"
			entry.ResolvedBy = fmt.Sprintf("auto_built_skill:%s", skillName)
			fmt.Printf("[AntigravityBridge] Auto-construction réussie : %s\n", msg)
			return fmt.Sprintf("J'ai signalé ce besoin à mon canal d'auto-évolution et j'ai créé automatiquement la nouvelle brique '%s' à chaud !", skillName), nil
		}
		fmt.Printf("[AntigravityBridge] Échec auto-construction : %v\n", err)
	}

	return fmt.Sprintf("Signal d'assistance transmis à mon canal de diagnostic : '%s'. Mon système d'auto-évolution s'en charge.", description), nil
}
