package agent

import (
	"context"
	"os"
	"testing"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type mockProjectProvider struct {
	response string
}

func (m *mockProjectProvider) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return m.response, nil
}

func (m *mockProjectProvider) GenerateStream(ctx context.Context, messages []llm.Message) (<-chan string, <-chan error) {
	out := make(chan string, 1)
	errs := make(chan error, 1)
	out <- m.response
	close(out)
	close(errs)
	return out, errs
}

func (m *mockProjectProvider) CreateEmbedding(ctx context.Context, text string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func TestProjectAgentCreate(t *testing.T) {
	os.Remove("active_project_test.mp")
	defer os.Remove("active_project_test.mp")

	projectManager := memory.NewProjectManager("active_project_test.mp")
	provider := &mockProjectProvider{
		response: `{
			"action": "create",
			"reason": "L'utilisateur veut écrire un livre.",
			"project": {
				"title": "Ecrire un Livre",
				"central_idea": "Un livre sur le code conscient",
				"goal": "Rediger 3 chapitres",
				"context": "Style amical",
				"current_step_id": "chapitre_1",
				"steps": [
					{ "id": "chapitre_1", "description": "Rediger le premier chapitre", "status": "active" },
					{ "id": "chapitre_2", "description": "Rediger le second chapitre", "status": "pending" }
				]
			}
		}`,
	}

	projectAgent := NewProjectAgent(provider, projectManager)

	if projectManager.GetActiveProject() != nil {
		t.Fatal("Active project should be nil initially")
	}

	projectAgent.AnalyzeAndManageProject(context.Background(), "Je veux écrire un livre sur le code conscient, on va faire ça ensemble", "Génial Marcelo ! Travaillons sur ton livre.")

	activeProj := projectManager.GetActiveProject()
	if activeProj == nil {
		t.Fatal("Active project should not be nil after create action")
	}

	if activeProj.Title != "Ecrire un Livre" || activeProj.CentralIdea != "Un livre sur le code conscient" {
		t.Fatalf("Unexpected project content: %+v", activeProj)
	}

	if len(activeProj.Steps) != 2 || activeProj.Steps[0].ID != "chapitre_1" || activeProj.Steps[0].Status != "active" {
		t.Fatalf("Unexpected steps layout: %+v", activeProj.Steps)
	}
}

func TestProjectAgentUpdate(t *testing.T) {
	os.Remove("active_project_test2.mp")
	defer os.Remove("active_project_test2.mp")

	projectManager := memory.NewProjectManager("active_project_test2.mp")
	initialProj := &memory.ProjectConfig{
		Title:         "Ecrire un Livre",
		CentralIdea:   "Un livre sur le code conscient",
		Goal:          "Rediger 3 chapitres",
		Context:       "Style amical",
		CurrentStepID: "chapitre_1",
		Steps: []memory.ProjectStep{
			{ID: "chapitre_1", Description: "Rediger le premier chapitre", Status: "active"},
			{ID: "chapitre_2", Description: "Rediger le second chapitre", Status: "pending"},
		},
		Status: "active",
	}
	projectManager.UpdateProject(initialProj)

	provider := &mockProjectProvider{
		response: `{
			"action": "update",
			"reason": "Le chapitre 1 est termine.",
			"project": {
				"title": "Ecrire un Livre",
				"central_idea": "Un livre sur le code conscient",
				"goal": "Rediger 3 chapitres",
				"context": "Style amical. Chapitre 1 rédigé.",
				"current_step_id": "chapitre_2",
				"steps": [
					{ "id": "chapitre_1", "description": "Rediger le premier chapitre", "status": "completed" },
					{ "id": "chapitre_2", "description": "Rediger le second chapitre", "status": "active" }
				]
			}
		}`,
	}

	projectAgent := NewProjectAgent(provider, projectManager)
	projectAgent.AnalyzeAndManageProject(context.Background(), "Le premier chapitre est fini, passons au deuxieme !", "Excellent, j'ai note le chapitre 1 comme termine. Passons au second.")

	activeProj := projectManager.GetActiveProject()
	if activeProj == nil {
		t.Fatal("Active project should not be nil after update action")
	}

	if activeProj.CurrentStepID != "chapitre_2" {
		t.Fatalf("Expected active step to be 'chapitre_2', got '%s'", activeProj.CurrentStepID)
	}

	if activeProj.Steps[0].Status != "completed" || activeProj.Steps[1].Status != "active" {
		t.Fatalf("Unexpected steps layout after update: %+v", activeProj.Steps)
	}
}

func TestProjectAgentClose(t *testing.T) {
	os.Remove("active_project_test3.mp")
	defer os.Remove("active_project_test3.mp")

	projectManager := memory.NewProjectManager("active_project_test3.mp")
	initialProj := &memory.ProjectConfig{
		Title:       "Ecrire un Livre",
		CentralIdea: "Un livre sur le code conscient",
		Goal:        "Rediger 3 chapitres",
		Status:      "active",
	}
	projectManager.UpdateProject(initialProj)

	provider := &mockProjectProvider{
		response: `{
			"action": "close",
			"reason": "L'utilisateur veut passer à autre chose."
		}`,
	}

	projectAgent := NewProjectAgent(provider, projectManager)
	projectAgent.AnalyzeAndManageProject(context.Background(), "On arrete le livre", "Pas de probleme.")

	if projectManager.GetActiveProject() != nil {
		t.Fatal("Active project should be nil after close action")
	}
}
