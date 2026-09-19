package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/marce555/pixel/internal/agent"
	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/scheduler"
)

type consoleBroadcaster struct{}

func (b *consoleBroadcaster) Broadcast(msg string) {
	fmt.Printf("\n[BROADCAST]\n%s\n", msg)
}

type dummySTM struct{}

func (d *dummySTM) AddMessage(msg llm.Message) {}

func loadEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, `"'`)
			os.Setenv(key, val)
		}
	}
}

func main() {
	loadEnv()

	filter := "Choc des Géants"
	if len(os.Args) > 1 {
		filter = strings.Join(os.Args[1:], " ")
	}

	fmt.Printf("=== Démarrage de l'Audit & Correction pour : %q ===\n", filter)

	coreMem := memory.NewCoreMemory("pixel_core_memory.json")
	llmSettings := coreMem.GetLLMSettings()
	provider := llm.NewAdaptiveProvider(
		llmSettings.ActiveMode,
		llmSettings.CloudAPIKey,
		llmSettings.CloudBaseURL,
		llmSettings.CloudModel,
		llmSettings.LocalBaseURL,
		llmSettings.LocalModel,
	)

	draftManager := scheduler.NewDraftManager("drafts")
	reviewer := agent.NewReviewerAgent(provider)

	handler := scheduler.NewAuditAndFixArticlesHandler(
		&consoleBroadcaster{},
		&dummySTM{},
		provider,
		draftManager,
		reviewer,
	)

	task := &scheduler.Task{
		ID:          "audit_manual_run",
		Type:        "audit_and_fix_articles",
		Payload:     filter,
		Status:      scheduler.StatusRunning,
		ScheduledAt: time.Now(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	err := handler(ctx, task)
	if err != nil {
		fmt.Printf("\n❌ Erreur lors de l'opération : %v\n", err)
	} else {
		fmt.Println("\n✅ Opération terminée avec succès !")
	}

	fmt.Println("\n=== LOGS DÉTAILLÉS DE LA TÂCHE ===")
	fmt.Print(task.GetLog())
}
