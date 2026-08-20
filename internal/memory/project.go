package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

type ProjectStep struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Status      string    `json:"status"` // "pending" | "active" | "completed"
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

type ProjectConfig struct {
	Title         string        `json:"title"`
	CentralIdea   string        `json:"central_idea"`
	Goal          string        `json:"goal"`
	Context       string        `json:"context"` // Accumulated context notes
	CurrentStepID string        `json:"current_step_id"`
	Steps         []ProjectStep `json:"steps"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Status        string        `json:"status"` // "active" | "completed" | "paused"
}

type ProjectManager struct {
	mu            sync.RWMutex
	filePath      string
	ActiveProject *ProjectConfig
}

func NewProjectManager(filePath string) *ProjectManager {
	pm := &ProjectManager{
		filePath: filePath,
	}
	pm.Load()
	return pm
}

func (pm *ProjectManager) Load() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	data, err := os.ReadFile(pm.filePath)
	if err != nil {
		pm.ActiveProject = nil
		return
	}

	var proj ProjectConfig
	if err := json.Unmarshal(data, &proj); err != nil {
		fmt.Printf("[ProjectManager] Erreur parsing JSON: %v\n", err)
		pm.ActiveProject = nil
		return
	}

	if proj.Status == "active" {
		pm.ActiveProject = &proj
		fmt.Printf("[ProjectManager] Projet actif chargé : %s (Idée centrale: %s)\n", proj.Title, proj.CentralIdea)
	} else {
		pm.ActiveProject = nil
	}
}

func (pm *ProjectManager) Save() {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if pm.ActiveProject == nil {
		// Archiver ou supprimer le fichier actif
		return
	}

	data, err := json.MarshalIndent(pm.ActiveProject, "", "  ")
	if err != nil {
		fmt.Printf("[ProjectManager] Erreur de sauvegarde: %v\n", err)
		return
	}
	err = os.WriteFile(pm.filePath, data, 0644)
	if err != nil {
		fmt.Printf("[ProjectManager] Erreur écriture fichier: %v\n", err)
	}
}

func (pm *ProjectManager) GetActiveProject() *ProjectConfig {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if pm.ActiveProject == nil || pm.ActiveProject.Status != "active" {
		return nil
	}
	// Copie pour éviter les conditions de concurrence
	projCopy := *pm.ActiveProject
	if pm.ActiveProject.Steps != nil {
		projCopy.Steps = make([]ProjectStep, len(pm.ActiveProject.Steps))
		copy(projCopy.Steps, pm.ActiveProject.Steps)
	}
	return &projCopy
}

func (pm *ProjectManager) UpdateProject(proj *ProjectConfig) {
	pm.mu.Lock()
	if proj == nil {
		pm.ActiveProject = nil
		pm.mu.Unlock()
		os.Remove(pm.filePath)
		return
	}
	proj.UpdatedAt = time.Now()
	if proj.CreatedAt.IsZero() {
		proj.CreatedAt = time.Now()
	}
	pm.ActiveProject = proj
	pm.mu.Unlock()
	pm.Save()
}
