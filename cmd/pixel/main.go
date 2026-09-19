package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/marce555/pixel/internal/agent"
	"github.com/marce555/pixel/internal/api"
	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/resourceagent"
	"github.com/marce555/pixel/internal/scheduler"
	"github.com/marce555/pixel/internal/skills"
	"github.com/marce555/pixel/internal/timeagent"
)

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
	fmt.Println("Initialisation de Pixel, le compagnon conscient...")
	ctx := context.Background()

	// 1. Init Memory Subsystem
	coreMem := memory.NewCoreMemory("pixel_core_memory.json")
	stm := memory.NewSTM(8) // Limite de 8 messages avant déclenchement du sommeil
	ltm := memory.NewLTM()
	projectManager := memory.NewProjectManager("active_project.mp")

	// 2. Init LLM Adaptive Provider
	llmSettings := coreMem.GetLLMSettings()
	adaptiveProvider := llm.NewAdaptiveProvider(
		llmSettings.ActiveMode,
		llmSettings.CloudAPIKey,
		llmSettings.CloudBaseURL,
		llmSettings.CloudModel,
		llmSettings.LocalBaseURL,
		llmSettings.LocalModel,
	)
	provider := adaptiveProvider

	// 3. Init Sub-Agents
	rAgent := resourceagent.NewResourceAgent(stm)
	profiler := agent.NewProfilingAgent(provider, coreMem)
	projectAgent := agent.NewProjectAgent(provider, projectManager)

	// 4. Init SleepManager and start background loop
	sleepManager := agent.NewSleepManager(stm, ltm, coreMem, rAgent, profiler, provider)
	sleepManager.Start(ctx)

	// 5. Init Web Agent, EventHub, Tasks Scheduler and Thought Stream
	webAgent := agent.NewWebAgent()
	eventHub := api.NewEventHub()
	taskScheduler := scheduler.NewScheduler()
	taskScheduler.SetBroadcaster(eventHub)
	
	webResearcher := agent.NewWebResearcherAgent(provider, webAgent)
	sysadminAgent := agent.NewSysadminAgent(provider)
	
	draftManager := scheduler.NewDraftManager("drafts")
	reviewerAgent := agent.NewReviewerAgent(provider)

	// Register scheduler handlers
	taskScheduler.RegisterHandler("play_music", scheduler.NewPlayMusicHandler(webAgent, taskScheduler))
	taskScheduler.RegisterHandler("agent_task", scheduler.NewAgentTaskHandler(provider, webAgent, eventHub, stm))
	taskScheduler.RegisterHandler("research_task", agent.NewResearchTaskHandler(webResearcher, eventHub, stm))
	taskScheduler.RegisterHandler("sysadmin", agent.NewSysadminTaskHandler(sysadminAgent, eventHub, stm))
	taskScheduler.RegisterHandler("publish_article", scheduler.NewPublishArticleHandlerWithReviewer(eventHub, stm, provider, webAgent, draftManager, reviewerAgent))
	taskScheduler.RegisterHandler("depublish_duplicates", scheduler.NewDepublishDuplicatesHandler(eventHub, stm, provider))
	taskScheduler.RegisterHandler("audit_and_fix_articles", scheduler.NewAuditAndFixArticlesHandler(eventHub, stm, provider, draftManager, reviewerAgent))
	
	codeProvider := llm.NewOpenAICompatibleProvider(
		llmSettings.CodeBaseURL,
		llmSettings.CloudAPIKey, // in case the user decides to use a cloud model for code later
		llmSettings.CodeModel,
		"", // no embedding needed for code generation
	)
	
	skillManager := skills.NewSkillManager(codeProvider, "skills")
	taskScheduler.RegisterHandler("build_skill", skills.NewBuildSkillHandler(skillManager, eventHub.Broadcast))
	
	taskScheduler.Start(ctx)

	// Lancement de la synchronisation initiale en arrière-plan des articles publiés
	go func() {
		ctxSync, cancelSync := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancelSync()
		fmt.Println("[Main] Synchronisation initiale des articles publiés sur AppliYou...")
		if _, err := scheduler.SyncPublishedArticles(ctxSync); err != nil {
			fmt.Printf("[Main] Avertissement : échec de la synchronisation initiale des articles : %v\n", err)
		} else {
			fmt.Println("[Main] Synchronisation initiale des articles publiée réussie.")
		}
	}()

	thoughtStream := memory.NewThoughtStream(50)
	unconscious := memory.NewUnconsciousManager()
	superior := agent.NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, projectManager, taskScheduler, skillManager)
	superior.SetDraftManager(draftManager)

	// 6. Init Curiosity Agent & Thalamic Gate
	timeAgent := timeagent.NewTimeAgent()
	thalamicGate := agent.NewThalamicGate(provider)
	curiosityAgent := agent.NewCuriosityAgent(provider, coreMem, stm, ltm, eventHub, timeAgent, webAgent, thoughtStream, sleepManager, taskScheduler, thalamicGate)
	curiosityAgent.SetSkillManager(skillManager)

	// 7. Init VisionAgent and start background sensory vision loop
	visionAgent := agent.NewVisionAgent(provider, coreMem, ltm, stm, thoughtStream, func(msg string) {
		eventHub.Broadcast(msg)
	})
	visionAgent.Start(ctx)
	superior.SetVisionAgent(visionAgent)
	curiosityAgent.SetVisionAgent(visionAgent)
	curiosityAgent.Start(ctx)

	// 7b. Init GmailAgent and start background check loop
	gmailAgent := agent.NewGmailAgent(provider, coreMem, stm, ltm, thoughtStream, eventHub)
	gmailAgent.Start(ctx)

	// 8. Démarrage de l'Interface Web locale
	fmt.Println("Le Cerveau de Pixel tourne. Lancement de l'interface graphique...")
	webServer := api.NewServer(superior, coreMem, stm, sleepManager, profiler, projectAgent, projectManager, eventHub, taskScheduler, adaptiveProvider)
	log.Fatal(webServer.Start("8080"))
}
