package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/agent"
	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/resourceagent"
	"github.com/marce555/pixel/internal/scheduler"
	"github.com/marce555/pixel/internal/voice"
)

// EventHub manages SSE connections for proactive messages
type EventHub struct {
	clients map[chan string]bool
	mu      sync.Mutex
}

func NewEventHub() *EventHub {
	return &EventHub{
		clients: make(map[chan string]bool),
	}
}

func (h *EventHub) AddClient(client chan string) {
	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()
}

func (h *EventHub) RemoveClient(client chan string) {
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
}

func (h *EventHub) Broadcast(message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		client <- message
	}
}

func (h *EventHub) HasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

// Server handles the HTTP API and serves the web frontend.
type Server struct {
	superior         *agent.SuperiorAgent
	coreMem          *memory.CoreMemory
	stm              *memory.STM
	sleepManager     *agent.SleepManager
	profiler         *agent.ProfilingAgent
	projectAgent     *agent.ProjectAgent
	projectManager   *memory.ProjectManager
	EventHub         *EventHub
	scheduler        *scheduler.Scheduler
	voiceMgr         *voice.VoiceManager
	adaptiveProvider *llm.AdaptiveProvider
}

func NewServer(superior *agent.SuperiorAgent, coreMem *memory.CoreMemory, stm *memory.STM, sleepManager *agent.SleepManager, profiler *agent.ProfilingAgent, projectAgent *agent.ProjectAgent, projectManager *memory.ProjectManager, eventHub *EventHub, taskScheduler *scheduler.Scheduler, adaptiveProvider *llm.AdaptiveProvider) *Server {
	vm, err := voice.NewVoiceManager()
	if err != nil {
		fmt.Printf("[Server] Impossible d'initialiser le VoiceManager : %v\n", err)
	}

	return &Server{
		superior:         superior,
		coreMem:          coreMem,
		stm:              stm,
		sleepManager:     sleepManager,
		profiler:         profiler,
		projectAgent:     projectAgent,
		projectManager:   projectManager,
		EventHub:         eventHub,
		scheduler:        taskScheduler,
		voiceMgr:         vm,
		adaptiveProvider: adaptiveProvider,
	}
}

type chatRequest struct {
	Message string `json:"message"`
	Sender  string `json:"sender,omitempty"`
}

type chatResponse struct {
	Reply string `json:"reply"`
	Error string `json:"error,omitempty"`
}

// Start launches the HTTP server.
func (s *Server) Start(port string) error {
	http.Handle("/", http.FileServer(http.Dir("./web")))

	http.HandleFunc("/api/chat", s.handleChat)
	http.HandleFunc("/api/core_memory", s.handleCoreMemory)
	http.HandleFunc("/api/force_sleep", s.handleForceSleep)
	http.HandleFunc("/api/llm_settings", s.handleLLMSettings)
	http.HandleFunc("/api/gmail_settings", s.handleGmailSettings)
	http.HandleFunc("/api/events", s.handleEvents)
	http.HandleFunc("/api/ltm_learnings", s.handleLTMLearnings)
	http.HandleFunc("/api/history", s.handleHistory)
	http.HandleFunc("/api/system_status", s.handleSystemStatus)
	http.HandleFunc("/api/project", s.handleActiveProject)
	
	// Task Scheduler Routes
	http.HandleFunc("/api/tasks", s.handleTasks)
	http.HandleFunc("/api/tasks/cancel", s.handleTasksCancel)
	http.HandleFunc("/api/tasks/clear", s.handleTasksClear)

	// Local Models Route
	http.HandleFunc("/api/local-models", s.handleLocalModels)

	// Local Voice Routes
	http.HandleFunc("/api/voice/transcribe", s.handleVoiceTranscribe)
	http.HandleFunc("/api/voice/speak", s.handleVoiceSpeak)

	fmt.Printf("[Web Server] Interface prête sur http://localhost:%s\n", port)
	return http.ListenAndServe(":"+port, nil)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	clientChan := make(chan string)
	s.EventHub.AddClient(clientChan)

	defer func() {
		s.EventHub.RemoveClient(clientChan)
		close(clientChan)
	}()

	// Si le profil actif est actuellement Inconnu, lancer un scan d'identification visuelle d'ouverture
	if s.superior.GetVisionAgent() != nil && s.coreMem.GetProfile().Static.Name == "Inconnu" {
		go func() {
			// Petite pause pour laisser le canal SSE s'établir côté client
			time.Sleep(1 * time.Second)
			fmt.Println("[Server] Nouvelle connexion SSE avec profil Inconnu. Déclenchement du scan visuel d'ouverture...")
			s.superior.GetVisionAgent().ScanOnceOnOpening(context.Background())
		}()
	}

	for {
		select {
		case msg := <-clientChan:
			dataBytes, _ := json.Marshal(map[string]string{"proactive_message": msg})
			fmt.Fprintf(w, "data: %s\n\n", string(dataBytes))
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Sender != "" {
		s.coreMem.SwitchActiveProfile(req.Sender)
	}

	// Prioriser immédiatement le chat utilisateur en annulant les tâches de fond en cours (libère la GPU)
	s.sleepManager.CancelBackgroundTasks()

	// Si la mémoire STM est saturée, sauvegarder les messages pour les consolider après la génération de la réponse
	var msgsToConsolidate []llm.Message
	if s.stm.IsFull() {
		fmt.Println("[Server] STM saturée avant nouveau message. La consolidation d'urgence sera lancée après la réponse...")
		msgs := s.stm.GetMessages()
		// Consolider la première moitié des messages (les plus anciens)
		// et garder la seconde moitié pour préserver le contexte de la conversation.
		half := len(msgs) / 2
		msgsToConsolidate = msgs[:half]
		msgsToKeep := msgs[half:]

		s.stm.Clear()
		for _, msg := range msgsToKeep {
			s.stm.AddMessage(msg)
		}
	}

	// Ajouter à la mémoire immédiate
	s.stm.AddMessage(llm.Message{Role: llm.RoleUser, Content: req.Message})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Si le système était occupé à consolider, envoyer une note sympathique à l'utilisateur
	if s.sleepManager.IsBusy() {
		chunkBytes, _ := json.Marshal(map[string]string{"chunk": "*(Un instant, je termine de consolider mes souvenirs récents...)*\n\n"})
		fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
		flusher.Flush()
	}

	outChan, errChan := s.superior.ProcessInputStream(r.Context(), req.Message, s.stm.GetMessages())

	var fullReply string

	for {
		select {
		case chunk, ok := <-outChan:
			if !ok {
				outChan = nil
			} else {
				fullReply += chunk
				chunkBytes, _ := json.Marshal(map[string]string{"chunk": chunk})
				fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
				flusher.Flush()
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
			} else {
				errBytes, _ := json.Marshal(map[string]string{"error": err.Error()})
				fmt.Fprintf(w, "data: %s\n\n", string(errBytes))
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}

		if outChan == nil && errChan == nil {
			break
		}
	}

	if fullReply != "" {
		s.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: fullReply})
		// Indexation dynamique et tâches de fond lancées uniquement APRÈS l'envoi complet du flux pour éviter toute interférence
		bgCtx := s.sleepManager.AcquireBackgroundContext()
		
		go func(ctx context.Context) {
			// 1. Indexation en temps réel (contexte annulable)
			s.sleepManager.IndexExchange(ctx, req.Message, fullReply)

			if ctx.Err() != nil {
				return
			}
			// 2. Analyser de manière autonome le projet avec le ProjectAgent (contexte annulable)
			s.projectAgent.AnalyzeAndManageProject(ctx, req.Message, fullReply)

			if ctx.Err() != nil {
				return
			}
			// 3. Analyser métacognitivement si l'utilisateur corrige une erreur (contexte annulable)
			s.sleepManager.ReflectAndSelfCorrect(ctx, req.Message)

			if ctx.Err() != nil {
				return
			}
			// 4. Vérifier asynchroneusement si l'utilisateur donne une directive de comportement (contexte annulable)
			s.profiler.CheckForPersonaUpdate(ctx, req.Message)

			if ctx.Err() != nil {
				return
			}
			// 5. Consolider la STM si elle était saturée avant le message (contexte annulable)
			if len(msgsToConsolidate) > 0 {
				s.sleepManager.TriggerSleepCycleForMessages(ctx, msgsToConsolidate)
			}
		}(bgCtx)
	}
}

func (s *Server) handleCoreMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	profile := s.coreMem.GetProfile()
	data := map[string]interface{}{
		"agent_persona": s.coreMem.GetAgentPersona(),
		"user_profile":  profile,
		"dynamic_goals": s.coreMem.GetDynamicGoals(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) handleForceSleep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.sleepManager.TriggerSleepCycle(context.Background())
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status": "ok"}`))
}

func (s *Server) handleLTMLearnings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	recent := s.superior.GetRecentMemoriesWithDetails(10)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(recent)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	msgs := s.stm.GetMessages()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(msgs)
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := resourceagent.GetSystemStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleActiveProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	proj := s.projectManager.GetActiveProject()
	if proj == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error": "no active project"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(proj)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tasks := s.scheduler.GetTasks()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func (s *Server) handleTasksCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.scheduler.Cancel(req.ID); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"error": "%s"}`, err.Error())))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

func (s *Server) handleTasksClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.scheduler.ClearHistory()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
}

func (s *Server) handleVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fmt.Println("[Server] [Transcription] Requête de transcription reçue.")

	if s.voiceMgr == nil {
		fmt.Println("[Server] [Transcription] Erreur : s.voiceMgr est nul")
		http.Error(w, "Le module vocal local n'est pas initialisé", http.StatusInternalServerError)
		return
	}

	// Récupérer le fichier binaire du microphone
	file, _, err := r.FormFile("file")
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur FormFile : %v\n", err)
		http.Error(w, "Impossible de lire le fichier dans le formulaire : "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Créer un nom temporaire pour l'enregistrement webm dans /tmp
	tempWebm := filepath.Join(os.TempDir(), fmt.Sprintf("mic_%d.webm", time.Now().UnixNano()))
	out, err := os.Create(tempWebm)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur création fichier temp webm : %v\n", err)
		http.Error(w, "Impossible de créer le fichier audio temporaire : "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur écriture fichier temp webm : %v\n", err)
		http.Error(w, "Impossible de copier l'audio : "+err.Error(), http.StatusInternalServerError)
		return
	}
	out.Close() // Fermer avant l'appel à ffmpeg
	fmt.Printf("[Server] [Transcription] Audio enregistré dans : %s (taille : %d octets).\n", tempWebm, written)

	// Transcoder en WAV mono 16kHz via ffmpeg
	tempWav := filepath.Join(os.TempDir(), fmt.Sprintf("mic_%d.wav", time.Now().UnixNano()))

	err = s.voiceMgr.ConvertWebMToWav(tempWebm, tempWav)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur ffmpeg : %v\n", err)
		http.Error(w, "Échec du décodage audio via ffmpeg : "+err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Println("[Server] [Transcription] Décryptage et transcodage ffmpeg réussis.")

	// Envoyer le WAV décodé au serveur local Whisper
	text, err := s.voiceMgr.TranscribeAudio(r.Context(), tempWav)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur Whisper : %v\n", err)
		http.Error(w, "Échec de la transcription Whisper locale : "+err.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Printf("[Server] [Transcription] Résultat Whisper : '%s'\n", text)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": text})
}

func (s *Server) handleVoiceSpeak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.voiceMgr == nil {
		http.Error(w, "Le module vocal local n'est pas initialisé", http.StatusInternalServerError)
		return
	}

	text := r.URL.Query().Get("text")
	if text == "" {
		http.Error(w, "Le paramètre 'text' est requis", http.StatusBadRequest)
		return
	}

	// Synthétiser localement
	audioBytes, err := s.voiceMgr.Synthesize(r.Context(), text)
	if err != nil {
		http.Error(w, "Échec de la synthèse vocale locale : "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(audioBytes)))
	w.Write(audioBytes)
}

// handleLocalModels proxies the /v1/models call to the configured local server
// and returns a plain list of model IDs to the frontend.
func (s *Server) handleLocalModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Try flm list -j first as fallback for flm backend
	out, errCmd := exec.Command("/usr/local/bin/flm", "list", "-j").Output()
	if errCmd == nil {
		var flmResp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(out, &flmResp); err == nil {
			ids := make([]string, 0, len(flmResp.Models))
			for _, m := range flmResp.Models {
				ids = append(ids, m.Name)
			}
			json.NewEncoder(w).Encode(map[string][]string{"models": ids})
			return
		}
	}

	settings := s.coreMem.GetLLMSettings()
	localURL := strings.TrimSuffix(settings.LocalBaseURL, "/")

	resp, err := http.Get(localURL + "/models")
	if err != nil {
		w.Write([]byte(`{"models":[]}`))
		return
	}
	defer resp.Body.Close()

	var raw struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		w.Write([]byte(`{"models":[]}`))
		return
	}

	ids := make([]string, 0, len(raw.Data))
	for _, m := range raw.Data {
		ids = append(ids, m.ID)
	}
	json.NewEncoder(w).Encode(map[string][]string{"models": ids})
}

func (s *Server) handleLLMSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings := s.coreMem.GetLLMSettings()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(settings)
		return
	}

	if r.Method == http.MethodPost {
		var req memory.LLMSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.coreMem.UpdateLLMSettings(req)
		s.adaptiveProvider.UpdateSettings(
			req.ActiveMode,
			req.CloudAPIKey,
			req.CloudBaseURL,
			req.CloudModel,
			req.LocalBaseURL,
			req.LocalModel,
		)

		if s.superior != nil && s.superior.SkillManager() != nil {
			newCodeProvider := llm.NewOpenAICompatibleProvider(
				req.CodeBaseURL,
				req.CloudAPIKey,
				req.CodeModel,
				"",
			)
			s.superior.SkillManager().UpdateProvider(newCodeProvider)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleGmailSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings := s.coreMem.GetGmailSettings()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(settings)
		return
	}

	if r.Method == http.MethodPost {
		var req memory.GmailSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.coreMem.UpdateGmailSettings(req)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

