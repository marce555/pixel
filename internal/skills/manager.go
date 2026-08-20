package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
)

// SkillManifest represents the configuration of a dynamic skill
type SkillManifest struct {
	Name               string `json:"name"`
	Description        string `json:"description"`
	RouterInstructions string `json:"router_instructions"`
}

// SkillManager manages dynamic python/bash scripts that Pixel can use and create.
type SkillManager struct {
	mu          sync.RWMutex
	skills      map[string]SkillManifest
	skillsDir   string
	llmProvider llm.Provider
}

// NewSkillManager creates a new SkillManager and loads existing skills.
func NewSkillManager(provider llm.Provider, dir string) *SkillManager {
	if dir == "" {
		dir = "skills"
	}
	os.MkdirAll(dir, 0755)

	sm := &SkillManager{
		skills:      make(map[string]SkillManifest),
		skillsDir:   dir,
		llmProvider: provider,
	}
	sm.LoadSkills()
	return sm
}

// UpdateProvider dynamically updates the LLM Provider used for coding skills.
func (sm *SkillManager) UpdateProvider(provider llm.Provider) {
	sm.mu.Lock()
	sm.llmProvider = provider
	sm.mu.Unlock()
	fmt.Println("[SkillManager] Code Provider mis à jour dynamiquement.")
}

// LoadSkills scans the skills directory and registers them.
func (sm *SkillManager) LoadSkills() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Clear existing
	sm.skills = make(map[string]SkillManifest)

	entries, err := os.ReadDir(sm.skillsDir)
	if err != nil {
		fmt.Printf("[SkillManager] Erreur lecture dossier skills: %v\n", err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			manifestPath := filepath.Join(sm.skillsDir, entry.Name(), "manifest.json")
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var manifest SkillManifest
			if err := json.Unmarshal(data, &manifest); err == nil {
				// Assure le bon nommage
				manifest.Name = entry.Name()
				sm.skills[manifest.Name] = manifest
				fmt.Printf("[SkillManager] Skill chargé : %s\n", manifest.Name)
			}
		}
	}
}

// GetRouterPromptExtension returns the formatted text to append to the Router's ACTIONS DISPONIBLES.
func (sm *SkillManager) GetRouterPromptExtension() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.skills) == 0 {
		return ""
	}

	var ext strings.Builder
	ext.WriteString("\n\n--- ACTIONS DYNAMIQUES (SKILLS) ---\n")
	ext.WriteString("Tu peux également choisir l'une de ces actions personnalisées si la demande correspond :\n")

	for _, skill := range sm.skills {
		ext.WriteString(fmt.Sprintf("- \"skill_%s\" — %s\n", skill.Name, skill.RouterInstructions))
	}
	return ext.String()
}

// GetSkillsDescriptionForSystemPrompt returns a formatted string listing all available loaded skills/briques for Pixel's main LLM context prompt.
func (sm *SkillManager) GetSkillsDescriptionForSystemPrompt() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n--- 🧩 TES BRIQUES ET SKILLS DYNAMIQUES CHARGÉES (MCP) ---\n")
	sb.WriteString("Tu possèdes les briques autonomes (skills) suivantes chargées dans ton système, que tu peux exécuter :\n")

	for _, skill := range sm.skills {
		desc := skill.Description
		if desc == "" {
			desc = skill.RouterInstructions
		}
		sb.WriteString(fmt.Sprintf("- Brique '%s' : %s\n", skill.Name, desc))
	}
	sb.WriteString("----------------------------------------------------------\n")
	return sb.String()
}


// ExecuteSkill runs the skill's run.py with the provided query.
func (sm *SkillManager) ExecuteSkill(ctx context.Context, name string, query string) (string, error) {
	sm.mu.RLock()
	_, exists := sm.skills[name]
	sm.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("skill '%s' introuvable", name)
	}

	scriptPath := filepath.Join(sm.skillsDir, name, "run.py")
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return "", fmt.Errorf("script %s introuvable", scriptPath)
	}

	fmt.Printf("[SkillManager] Exécution du skill '%s' avec argument: '%s'\n", name, query)

	// Use a dedicated timeout (15 minutes) for long skill tasks like ISO flash/copy
	skillCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(skillCtx, "python3", "run.py", query)
	cmd.Dir = filepath.Join(sm.skillsDir, name)

	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		// Always include the combined output (stdout+stderr) in the error
		// so the LLM can see the actual error details instead of hallucinating.
		if result != "" {
			return "", fmt.Errorf("erreur d'exécution du skill '%s': %v\nDétails: %s", name, err, result)
		}
		return "", fmt.Errorf("erreur d'exécution du skill '%s': %v", name, err)
	}

	return result, nil
}

// BuildSkill autonomously creates a new skill using the Main LLM.
func (sm *SkillManager) BuildSkill(ctx context.Context, goalDescription string) (string, string, error) {
	fmt.Printf("[SkillManager] Démarrage de la construction autonome du skill pour: %s\n", goalDescription)

	prompt := fmt.Sprintf(`Tu es Pixel, un agent IA autonome. Tu dois coder un nouveau "Skill" (outil) en Python pour étendre tes propres capacités.
Objectif du skill demandé : %s

CRITÈRES DE SUCCÈS:
1. Tu dois écrire un script Python robuste ('run.py'). Il recevra un seul argument en ligne de commande (sys.argv[1]) qui est la "query" (la requête de l'utilisateur).
2. Le script doit imprimer (print) son résultat sur stdout. Seul le stdout sera lu.
3. Tu dois gérer les exceptions (try/except) pour ne pas crasher violemment.

FORMAT DE RÉPONSE OBLIGATOIRE :
Tu dois fournir EXACTEMENT DEUX blocs de code : d'abord le JSON du manifest, puis le code Python.

Bloc 1 (JSON) :
`+"```json"+`
{
  "name": "nom_du_skill_en_snake_case_court",
  "test_query": "exemple_argument",
  "description": "Description courte pour toi-même",
  "router_instructions": "Consigne stricte pour le Routeur (ex: 'Utilise cette action si l'utilisateur demande X. Mets Y dans query.')"
}
`+"```"+`

Bloc 2 (Python) :
`+"```python"+`
import sys
import os
import json
import subprocess
import re

def main():
    try:
        # L'argument passé par Pixel
        query = sys.argv[1] 
        
        # === IMPLÉMENTE LA LOGIQUE DE LA BRIQUE ICI ===
        # Rappel : Ton but est de résoudre l'objectif demandé.
        # Utilise print() pour renvoyer le résultat final à Pixel.
        
    except Exception as e:
        print(f'Erreur : {e}')

if __name__ == '__main__':
    main()
`+"```"+`

Ne mets aucun texte avant ou après ces deux blocs. Ton JSON doit être syntaxiquement parfait.`, goalDescription)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Génère le code du nouveau skill en JSON."},
	}

	maxRetries := 3
	var lastErr error
	var payload struct {
		Name               string `json:"name"`
		Description        string `json:"description"`
		RouterInstructions string `json:"router_instructions"`
		TestQuery          string `json:"test_query"`
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		fmt.Printf("[SkillManager] Génération LLM (Tentative %d/%d)...\n", attempt, maxRetries)
		resp, err := sm.llmProvider.Generate(ctx, messages)
		if err != nil {
			return "", "", fmt.Errorf("erreur génération LLM: %v", err)
		}

		resp = strings.TrimSpace(resp)

		// Extraire le bloc JSON
		var jsonStr string
		jsonRegex := regexp.MustCompile(`(?s)\x60\x60\x60(?:json)?\s*(\{.*?\})\s*\x60\x60\x60`)
		jsonMatches := jsonRegex.FindStringSubmatch(resp)
		if len(jsonMatches) > 1 {
			jsonStr = jsonMatches[1]
		} else {
			// fallback simple
			start := strings.Index(resp, "{")
			end := strings.LastIndex(resp, "}")
			if start != -1 && end != -1 && end > start {
				jsonStr = resp[start : end+1]
			}
		}

		// Extraire le bloc Python
		var pyStr string
		pyRegex := regexp.MustCompile(`(?s)\x60\x60\x60(?:python|py)\s*(.*?)\s*\x60\x60\x60`)
		pyMatches := pyRegex.FindStringSubmatch(resp)
		if len(pyMatches) > 1 {
			pyStr = pyMatches[1]
		}

		if jsonStr == "" || pyStr == "" {
			lastErr = fmt.Errorf("impossible d'extraire les deux blocs (JSON et Python)")
			fmt.Printf("[SkillManager] Format invalide. Demande de correction.\n")
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: resp})
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "Tu n'as pas fourni les deux blocs markdown distincts. Fournis EXACTEMENT un bloc ```json et un bloc ```python."})
			continue
		}

		// Les LLMs échappent souvent les apostrophes en français (\') dans le JSON, ce qui est invalide.
		jsonStr = strings.ReplaceAll(jsonStr, `\'`, `'`)

		err = json.Unmarshal([]byte(jsonStr), &payload)
		if err != nil {
			lastErr = fmt.Errorf("erreur parsing JSON du LLM: %v", err)
			fmt.Printf("[SkillManager] Erreur JSON: %v. Demande de correction.\n", err)
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: resp})
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Ton bloc JSON est invalide : %v. Corrige et renvoie les deux blocs.", err)})
			continue
		}

		if payload.Name == "" {
			lastErr = fmt.Errorf("le LLM a retourné un nom vide dans le JSON")
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: resp})
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "Le champ 'name' est vide dans le JSON. Recommence."})
			continue
		}

		// Créer temporairement la brique pour la tester
		skillPath := filepath.Join(sm.skillsDir, payload.Name)
		os.MkdirAll(skillPath, 0755)
		scriptPath := filepath.Join(skillPath, "run.py")
		os.WriteFile(scriptPath, []byte(pyStr), 0755)

		// Phase de Test Automatique
		fmt.Printf("[SkillManager] Test du code généré avec l'argument: '%s'...\n", payload.TestQuery)
		testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		cmd := exec.CommandContext(testCtx, "python3", "run.py", payload.TestQuery)
		cmd.Dir = skillPath
		output, execErr := cmd.CombinedOutput()
		cancel()

		if execErr != nil {
			lastErr = fmt.Errorf("exécution échouée: %v\nSortie: %s", execErr, string(output))
			fmt.Printf("[SkillManager] Test échoué: %v\n", lastErr)
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: resp})
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Le script a généré une erreur lors du test avec '%s' :\n%s\nCorrige le code python_code et renvoie le JSON complet.", payload.TestQuery, lastErr.Error())})
			continue
		}

		// Test réussi ! On sauvegarde le manifest et on charge.
		fmt.Printf("[SkillManager] Test réussi ! Sortie: %s\n", string(output))
		
		manifest := SkillManifest{
			Name: payload.Name,
			Description: payload.Description,
			RouterInstructions: payload.RouterInstructions,
		}
		
		manifestData, _ := json.MarshalIndent(manifest, "", "  ")
		os.WriteFile(filepath.Join(skillPath, "manifest.json"), manifestData, 0644)
		
		sm.LoadSkills()
		return fmt.Sprintf("Brique '%s' créée, testée avec succès, et chargée à chaud ! Le routeur peut maintenant l'utiliser.", payload.Name), payload.Name, nil
	}

	return "", "", fmt.Errorf("impossible de créer la brique après %d tentatives. Dernière erreur : %v", maxRetries, lastErr)
}
