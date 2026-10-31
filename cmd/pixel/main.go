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
	"github.com/marce555/pixel/internal/voice"
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
	resourceagent.AddLiveLog("Système", "Initialisation du Cerveau de Pixel...")
	ctx := context.Background()

	// 1. Init Memory Subsystem
	coreMem := memory.NewCoreMemory("pixel_core_memory.json")
	stm := memory.NewSTM(8) // Limite de 8 messages avant déclenchement du sommeil
	ltm := memory.NewLTM()
	projectManager := memory.NewProjectManager("active_project.mp")

	// 2. Init Multi-Accelerator LLM Providers (dGPU CUDA :52625 + iGPU Vulkan :18081 + NPU XDNA2 :18082)
	llmSettings := coreMem.GetLLMSettings()
	// Primary Chat & Vision Provider -> dGPU NVIDIA RTX 5060 (CUDA, port 52625)
	adaptiveProvider := llm.NewAdaptiveProvider(
		llmSettings.ActiveMode,
		llmSettings.CloudAPIKey,
		llmSettings.CloudBaseURL,
		llmSettings.CloudModel,
		llmSettings.LocalBaseURL,
		llmSettings.LocalModel,
	)
	provider := adaptiveProvider

	// Fast Parallel Router & Heavy Background Agents -> iGPU AMD Radeon 890M (Vulkan0, port 18081)
	igpuBaseURL := os.Getenv("PIXEL_IGPU_URL")
	if igpuBaseURL == "" {
		igpuBaseURL = "http://127.0.0.1:18081/v1"
	}
	igpuModel := os.Getenv("PIXEL_IGPU_MODEL")
	if igpuModel == "" {
		igpuModel = "qwen3.5:9b"
	}
	igpuProvider := llm.NewAdaptiveProvider(
		llmSettings.ActiveMode,
		llmSettings.CloudAPIKey,
		llmSettings.CloudBaseURL,
		llmSettings.CloudModel,
		igpuBaseURL,
		igpuModel,
	)
	igpuProvider.SetLocalFallback(llmSettings.LocalBaseURL, llmSettings.LocalModel)

	// Continuous Cognitive Subconscious Agents -> NPU AMD Ryzen AI XDNA 2 (FastFlowLM, port 18082)
	npuBaseURL := os.Getenv("PIXEL_NPU_URL")
	if npuBaseURL == "" {
		npuBaseURL = "http://127.0.0.1:18082/v1"
	}
	npuModel := os.Getenv("PIXEL_NPU_MODEL")
	if npuModel == "" {
		npuModel = "qwen3.5:2b"
	}
	npuProvider := llm.NewAdaptiveProvider(
		llmSettings.ActiveMode,
		llmSettings.CloudAPIKey,
		llmSettings.CloudBaseURL,
		llmSettings.CloudModel,
		npuBaseURL,
		npuModel,
	)
	npuProvider.SetLocalFallback(igpuBaseURL, igpuModel)

	// 3. Init Sub-Agents (Offloaded to NPU / iGPU so primary dGPU stays 100% free for user chat)
	rAgent := resourceagent.NewResourceAgent(stm)
	profiler := agent.NewProfilingAgent(npuProvider, coreMem)
	projectAgent := agent.NewProjectAgent(npuProvider, projectManager)

	// 4. Init SleepManager and start background loop (NPU)
	sleepManager := agent.NewSleepManager(stm, ltm, coreMem, rAgent, profiler, npuProvider)
	sleepManager.Start(ctx)

	// 5. Init Web Agent, EventHub, Tasks Scheduler and Thought Stream
	webAgent := agent.NewWebAgent()
	eventHub := api.NewEventHub()
	taskScheduler := scheduler.NewScheduler()
	taskScheduler.SetBroadcaster(eventHub)

	// VoiceManager for TTS and Radio DJ announcements
	voiceMgr, err := voice.NewVoiceManager()
	if err != nil {
		fmt.Printf("[Main] Avertissement initialisation VoiceManager : %v\n", err)
	} else {
		taskScheduler.SetVoiceSpeaker(voiceMgr)
	}

	webResearcher := agent.NewWebResearcherAgent(igpuProvider, webAgent)
	sysadminAgent := agent.NewSysadminAgent(igpuProvider)

	draftManager := scheduler.NewDraftManager("drafts")
	reviewerAgent := agent.NewReviewerAgent(igpuProvider)
	// Agent Vérificateur indépendant : isolé sur l'iGPU (ou Cloud si mode cloud) pour ne jamais bloquer le chat principal
	factCheckerAgent := agent.NewFactCheckerAgent(igpuProvider, webAgent)
	// Agent de Police Bibliographique : traque les chimères statistiques, co-auteurs fictifs et affiliations erronées
	biblioAuditorAgent := agent.NewBibliographicAuditorAgent(igpuProvider, webAgent)

	// Register scheduler handlers
	taskScheduler.RegisterHandler("play_music", scheduler.NewPlayMusicHandler(webAgent, taskScheduler))
	taskScheduler.RegisterHandler("agent_task", scheduler.NewAgentTaskHandler(igpuProvider, webAgent, eventHub, stm))
	taskScheduler.RegisterHandler("research_task", agent.NewResearchTaskHandler(webResearcher, eventHub, stm, ltm))
	taskScheduler.RegisterHandler("sysadmin", agent.NewSysadminTaskHandler(sysadminAgent, eventHub, stm))
	taskScheduler.RegisterHandler("publish_article", scheduler.NewPublishArticleHandlerWithBibliographicAuditor(eventHub, stm, igpuProvider, webAgent, draftManager, reviewerAgent, factCheckerAgent, biblioAuditorAgent))
	taskScheduler.RegisterHandler("depublish_duplicates", scheduler.NewDepublishDuplicatesHandler(eventHub, stm, igpuProvider))
	taskScheduler.RegisterHandler("audit_and_fix_articles", scheduler.NewAuditAndFixArticlesHandlerWithBibliographicAuditor(eventHub, stm, igpuProvider, draftManager, reviewerAgent, factCheckerAgent, biblioAuditorAgent))

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
	selfAwareness := agent.NewSelfAwarenessAgent(npuProvider, coreMem, ltm, thoughtStream, projectManager, unconscious, "pixel_self_knowledge.json")

	superior := agent.NewSuperiorAgent(provider, coreMem, ltm, webAgent, thoughtStream, unconscious, projectManager, taskScheduler, skillManager)
	superior.SetFastProvider(igpuProvider)
	superior.SetBackgroundProvider(npuProvider)
	superior.SetDraftManager(draftManager)
	superior.SetSelfAwareness(selfAwareness)

	sleepManager.SetSelfAwareness(selfAwareness)

	// 6. Init Curiosity Agent (iGPU) & Thalamic Gate (NPU)
	timeAgent := timeagent.NewTimeAgent()
	thalamicGate := agent.NewThalamicGate(npuProvider)
	curiosityAgent := agent.NewCuriosityAgent(igpuProvider, coreMem, stm, ltm, eventHub, timeAgent, webAgent, thoughtStream, sleepManager, taskScheduler, thalamicGate)
	curiosityAgent.SetSkillManager(skillManager)
	curiosityAgent.SetSelfAwareness(selfAwareness)

	// 7. Init VisionAgent (dGPU multimodal) and start background sensory vision loop
	visionAgent := agent.NewVisionAgent(provider, coreMem, ltm, stm, thoughtStream, func(msg string) {
		eventHub.Broadcast(msg)
	})
	visionAgent.Start(ctx)
	superior.SetVisionAgent(visionAgent)
	curiosityAgent.SetVisionAgent(visionAgent)
	curiosityAgent.Start(ctx)

	// 7b. Init GmailAgent (NPU) and start background check loop
	gmailAgent := agent.NewGmailAgent(npuProvider, coreMem, stm, ltm, thoughtStream, eventHub)
	gmailAgent.Start(ctx)

	// 7c. Init ServerMonitorAgent (NPU) and start background hourly monitoring loop on appliyou.fr
	serverMonitorAgent := agent.NewServerMonitorAgent(npuProvider, coreMem, stm, ltm, thoughtStream, eventHub, skillManager)
	serverMonitorAgent.Start(ctx)

	// 8. Démarrage de l'Interface Web locale
	fmt.Println("Le Cerveau de Pixel tourne. Lancement de l'interface graphique...")
	webServer := api.NewServer(superior, coreMem, stm, sleepManager, profiler, projectAgent, projectManager, eventHub, taskScheduler, adaptiveProvider)
	log.Fatal(webServer.Start("8080"))
}
