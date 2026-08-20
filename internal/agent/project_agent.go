package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type ProjectAgent struct {
	llmProvider    llm.Provider
	projectManager *memory.ProjectManager
}

func NewProjectAgent(provider llm.Provider, manager *memory.ProjectManager) *ProjectAgent {
	return &ProjectAgent{
		llmProvider:    provider,
		projectManager: manager,
	}
}

type ProjectAnalysisResult struct {
	Action  string                `json:"action"` // "create" | "update" | "close" | "none"
	Reason  string                `json:"reason"`
	Project *memory.ProjectConfig `json:"project,omitempty"`
}

func (pa *ProjectAgent) AnalyzeAndManageProject(ctx context.Context, userMsg, assistantReply string) {
	activeProj := pa.projectManager.GetActiveProject()

	// Préparer l'historique d'échange récent
	exchangeText := fmt.Sprintf("Utilisateur: %s\nPixel: %s", userMsg, assistantReply)

	var systemPrompt string
	if activeProj == nil {
		// Aucun projet actif, détecter si on en lance un
		systemPrompt = `Tu es l'Agent de Projet de Pixel. Analyse la discussion et réponds UNIQUEMENT et STRICTEMENT par un objet JSON valide.

Si l'utilisateur lance, continue, reprend ou accepte de travailler sur un projet structuré à long terme (ex: écrire un livre, développer un logiciel, planifier un événement, etc.), réponds avec action="create" et configure le projet.
Sinon, réponds avec action="none".

Format de réponse attendu pour "create" :
{
  "action": "create",
  "reason": "Explication courte du lancement",
  "project": {
    "title": "Titre du Projet (ex: Ecriture de mon Livre)",
    "central_idea": "L'idée centrale du projet en une phrase",
    "goal": "L'objectif final à accomplir",
    "context": "Style, directives et contexte général",
    "current_step_id": "etape1",
    "steps": [
      { "id": "etape1", "description": "Première étape concrète", "status": "active" },
      { "id": "etape2", "description": "Deuxième étape concrète", "status": "pending" },
      { "id": "etape3", "description": "Troisième étape concrète", "status": "pending" }
    ]
  }
}

Format de réponse attendu pour "none" :
{
  "action": "none",
  "reason": "Pas de projet structuré détecté."
}

RÈGLES CRITIQUES :
1. Ne génère aucun texte d'introduction ni de conclusion. Réponds uniquement par le JSON.
2. Entoure obligatoirement toutes les clés et valeurs de type chaîne par des doubles guillemets.
3. Sois concis dans les descriptions pour éviter d'être coupé.`
	} else {
		// Projet déjà actif, détecter s'il faut le mettre à jour ou le clore
		projBytes, _ := json.Marshal(activeProj)
		systemPrompt = fmt.Sprintf(`Tu es l'Agent de Projet de Pixel. Le projet suivant est actuellement ACTIF :
%s

Analyse l'échange actuel :
- Si l'utilisateur demande explicitement d'arrêter le projet ou de passer à autre chose, réponds avec action="close".
- Si le projet progresse (étape finie, notes ajoutées, plan modifié), mets à jour le projet et réponds avec action="update".
- Sinon (simple discussion), réponds avec action="none".

Format de réponse pour "update" :
{
  "action": "update",
  "reason": "Explication de la mise à jour",
  "project": { ... l'objet project entier mis à jour ... }
}

Format de réponse pour "close" :
{
  "action": "close",
  "reason": "Explication de la clôture"
}

Format de réponse pour "none" :
{
  "action": "none",
  "reason": "Pas de changement structurel."
}

RÈGLES CRITIQUES :
1. Réponds uniquement en JSON valide, sans introduction ni conclusion.
2. Toutes les clés et valeurs doivent être entourées de doubles guillemets.`, string(projBytes))
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: "Voici la dernière interaction de la conversation :\n" + exchangeText},
	}

	responseJSON, err := pa.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[ProjectAgent] Erreur d'appel LLM: %v\n", err)
		return
	}

	responseJSON = extractJSON(responseJSON)
	responseJSON = repairJSON(responseJSON)

	var analysis ProjectAnalysisResult
	if err := json.Unmarshal([]byte(responseJSON), &analysis); err != nil {
		fmt.Printf("[ProjectAgent] Erreur parsing JSON : %v\nJSON brut: %s\n", err, responseJSON)
		return
	}

	switch analysis.Action {
	case "create":
		if analysis.Project != nil {
			analysis.Project.Status = "active"
			pa.projectManager.UpdateProject(analysis.Project)
			fmt.Printf("[ProjectAgent] Nouveau projet détecté et auto-programmé : '%s' (%s)\n", analysis.Project.Title, analysis.Reason)
		}
	case "update":
		if analysis.Project != nil {
			analysis.Project.Status = "active"
			pa.projectManager.UpdateProject(analysis.Project)
			fmt.Printf("[ProjectAgent] Projet mis à jour avec succès (%s)\n", analysis.Reason)
		}
	case "close":
		fmt.Printf("[ProjectAgent] Signal de clôture de projet reçu (%s)\n", analysis.Reason)
		if activeProj != nil {
			activeProj.Status = "completed"
			pa.projectManager.UpdateProject(nil) // Supprime le projet actif de active_project.mp
		}
	case "none":
		// Rien à faire
	default:
		fmt.Printf("[ProjectAgent] Action inconnue: %s\n", analysis.Action)
	}
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return s
	}
	return s[start : end+1]
}
