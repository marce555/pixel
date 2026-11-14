package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		select {
		case client <- message:
		default:
			// Client channel plein ou lent : ne bloque pas les autres clients ni le serveur
		}
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
	publishProvider  *llm.AdaptiveProvider
	reviewProvider   *llm.AdaptiveProvider
	igpuBaseURL      string
	igpuModel        string
}

func (s *Server) SetTaskProviders(publishProvider, reviewProvider *llm.AdaptiveProvider, igpuBaseURL, igpuModel string) {
	s.publishProvider = publishProvider
	s.reviewProvider = reviewProvider
	s.igpuBaseURL = igpuBaseURL
	s.igpuModel = igpuModel
}

func NewServer(superior *agent.SuperiorAgent, coreMem *memory.CoreMemory, stm *memory.STM, sleepManager *agent.SleepManager, profiler *agent.ProfilingAgent, projectAgent *agent.ProjectAgent, projectManager *memory.ProjectManager, eventHub *EventHub, taskScheduler *scheduler.Scheduler, adaptiveProvider *llm.AdaptiveProvider) *Server {
	var vm *voice.VoiceManager
	if taskScheduler != nil {
		if sp := taskScheduler.GetVoiceSpeaker(); sp != nil {
			if v, ok := sp.(*voice.VoiceManager); ok {
				vm = v
			}
		}
	}
	if vm == nil {
		var err error
		vm, err = voice.NewVoiceManager()
		if err != nil {
			fmt.Printf("[Server] Impossible d'initialiser le VoiceManager : %v\n", err)
		}
	}

	if stm != nil {
		stm.RemoveGeminiExchanges()
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
	Device  string `json:"device,omitempty"` // "pc" (default) or "android" / "mobile"
}

type chatResponse struct {
	Reply string `json:"reply"`
	Error string `json:"error,omitempty"`
}

// Start launches the HTTP server.
func (s *Server) Start(port string) error {
	mux := http.NewServeMux()

	mux.Handle("/", http.FileServer(http.Dir("./web")))

	mux.HandleFunc("/api/ping", s.handlePing)
	mux.HandleFunc("/api/apk", s.handleDownloadAPK)
	mux.HandleFunc("/api/chat", s.handleChat)
	mux.HandleFunc("/api/core_memory", s.handleCoreMemory)
	mux.HandleFunc("/api/core_memory/goal", s.handleCoreMemoryGoal)
	mux.HandleFunc("/api/core_memory/volatile", s.handleCoreMemoryVolatile)
	mux.HandleFunc("/api/core_memory/reload", s.handleCoreMemoryReload)
	mux.HandleFunc("/api/force_sleep", s.handleForceSleep)
	mux.HandleFunc("/api/llm_settings", s.handleLLMSettings)
	mux.HandleFunc("/api/gmail_settings", s.handleGmailSettings)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/ltm_learnings", s.handleLTMLearnings)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/system_status", s.handleSystemStatus)
	mux.HandleFunc("/api/project", s.handleActiveProject)

	// Task Scheduler Routes
	mux.HandleFunc("/api/tasks", s.handleTasks)
	mux.HandleFunc("/api/tasks/cancel", s.handleTasksCancel)
	mux.HandleFunc("/api/tasks/clear", s.handleTasksClear)

	// Local & Cloud Models Routes
	mux.HandleFunc("/api/local-models", s.handleLocalModels)
	mux.HandleFunc("/api/cloud-models", s.handleCloudModels)

	// Local Voice Routes
	mux.HandleFunc("/api/voice/transcribe", s.handleVoiceTranscribe)
	mux.HandleFunc("/api/voice/speak", s.handleVoiceSpeak)

	// Media Control Routes (Audio Ducking, Streaming)
	mux.HandleFunc("/api/media/duck", s.handleMediaDuck)
	mux.HandleFunc("/api/media/stream_ended", s.handleMediaStreamEnded)
	mux.HandleFunc("/api/media/stop", s.handleMediaStop)

	if strings.TrimSpace(os.Getenv("PIXEL_API_TOKEN")) != "" {
		fmt.Println("[Security] Protection par Token activée pour toutes les connexions externes (hors localhost).")
	} else {
		fmt.Println("[Security] PIXEL_API_TOKEN non défini : seules les connexions locales (127.0.0.1 / ::1) sont autorisées.")
	}

	certFile := strings.TrimSpace(os.Getenv("PIXEL_TLS_CERT"))
	keyFile := strings.TrimSpace(os.Getenv("PIXEL_TLS_KEY"))
	if certFile == "" && keyFile == "" {
		defaultCert := "certs/server.crt"
		defaultKey := "certs/server.key"
		if _, errC := os.Stat(defaultCert); errC == nil {
			if _, errK := os.Stat(defaultKey); errK == nil {
				certFile = defaultCert
				keyFile = defaultKey
			}
		}
	}

	if certFile != "" && keyFile != "" {
		tlsPort := strings.TrimSpace(os.Getenv("PIXEL_TLS_PORT"))
		if tlsPort == "" {
			tlsPort = "8443"
		}

		if tlsPort != port {
			// Lancer l'interface HTTP standard en tâche de fond pour pixel_gui.py et compatibilité locale
			go func() {
				fmt.Printf("[Web Server] Interface HTTP locale prête sur http://localhost:%s\n", port)
				if err := http.ListenAndServe(":"+port, s.authMiddleware(mux)); err != nil && err != http.ErrServerClosed {
					fmt.Printf("[Web Server] Erreur serveur HTTP : %v\n", err)
				}
			}()
		}

		// Écouter également le port 38443 pour correspondre directement à la redirection Freebox si configurée en 1:1
		if tlsPort == "8443" {
			go func() {
				fmt.Printf("[Web Server] Interface HTTPS secondaire prête sur https://0.0.0.0:38443\n")
				_ = http.ListenAndServeTLS(":38443", certFile, keyFile, s.authMiddleware(mux))
			}()
		}

		fmt.Printf("[Web Server] Interface HTTPS (TLS chiffré) prête sur https://0.0.0.0:%s (cert: %s)\n", tlsPort, certFile)
		return http.ListenAndServeTLS(":"+tlsPort, certFile, keyFile, s.authMiddleware(mux))
	}

	fmt.Printf("[Web Server] Interface prête sur http://localhost:%s\n", port)
	return http.ListenAndServe(":"+port, s.authMiddleware(mux))
}

func isLocalRequest(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-Ip") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func extractRequestToken(r *http.Request) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	if headerToken := strings.TrimSpace(r.Header.Get("X-Pixel-Token")); headerToken != "" {
		return headerToken
	}
	if queryToken := strings.TrimSpace(r.URL.Query().Get("token")); queryToken != "" {
		return queryToken
	}
	return ""
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Autoriser le téléchargement direct de l'APK d'installation / mise à jour sans token
		if r.URL.Path == "/api/apk" {
			next.ServeHTTP(w, r)
			return
		}

		// Autoriser les connexions locales (PC hôte : 127.0.0.1 / ::1) sans friction
		if isLocalRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Pour toute connexion externe (Freebox / Mobile / LAN), exiger PIXEL_API_TOKEN
		expectedToken := strings.TrimSpace(os.Getenv("PIXEL_API_TOKEN"))
		if expectedToken == "" {
			fmt.Printf("[Security] Connexion externe bloquée (PIXEL_API_TOKEN non configuré) depuis %s (%s)\n", r.RemoteAddr, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error": "remote access disabled: PIXEL_API_TOKEN is not configured"}`))
			return
		}

		providedToken := extractRequestToken(r)
		if providedToken == "" || subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) != 1 {
			fmt.Printf("[Security] Accès externe refusé (Token invalide ou manquant) depuis %s (%s)\n", r.RemoteAddr, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": "unauthorized: invalid or missing token"}`))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	profileName := "Inconnu"
	if s.coreMem != nil {
		profileName = s.coreMem.GetProfile().Static.Name
	}
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"agent":   "Pixel",
		"profile": profileName,
	})
}

func (s *Server) handleDownloadAPK(w http.ResponseWriter, r *http.Request) {
	apkPath := filepath.Join("android", "pixel-companion.apk")
	if _, err := os.Stat(apkPath); os.IsNotExist(err) {
		http.Error(w, "Fichier APK introuvable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="pixel-companion.apk"`)
	http.ServeFile(w, r, apkPath)
}

func isInternalThought(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if strings.HasPrefix(lower, "[pensée]") || strings.HasPrefix(lower, "[pensee]") ||
		strings.HasPrefix(lower, "pensée") || strings.HasPrefix(lower, "pensee") ||
		strings.HasPrefix(lower, "[vision]") || strings.HasPrefix(lower, "vision:") ||
		strings.HasPrefix(lower, "[curiosity]") || strings.HasPrefix(lower, "[curiosityagent]") ||
		strings.HasPrefix(lower, "*(pensée") || strings.HasPrefix(lower, "*(pensee") ||
		strings.HasPrefix(lower, "pensée de pixel") || strings.HasPrefix(lower, "pensee de pixel") ||
		strings.Contains(lower, "<think>") {
		return true
	}
	// Mode surveillance : ne jamais notifier les écrans noirs, scènes sombres ou l'absence de personne sur le smartphone
	if agent.IsEmptyOrBlackVision(msg) ||
		strings.Contains(lower, "image complètement noire") || strings.Contains(lower, "image noire") ||
		strings.Contains(lower, "écran noir") || strings.Contains(lower, "sans personne") ||
		strings.Contains(lower, "ne vois clairement personne") || strings.Contains(lower, "aucune personne") ||
		strings.Contains(lower, "nouveau visage face à ma caméra que je ne reconnais pas... qui es-tu") {
		return true
	}
	return false
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

	isAndroid := strings.Contains(strings.ToLower(r.UserAgent()), "android") ||
		r.Header.Get("X-Pixel-Device") == "android" ||
		r.URL.Query().Get("device") == "android"

	clientChan := make(chan string, 100)
	s.EventHub.AddClient(clientChan)

	defer func() {
		s.EventHub.RemoveClient(clientChan)
		close(clientChan)
	}()

	// Battement de cœur SSE toutes les 15s pour maintenir active la table NAT Freebox et le réseau mobile
	keepAliveTicker := time.NewTicker(15 * time.Second)
	defer keepAliveTicker.Stop()

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
			if isAndroid && isInternalThought(msg) {
				continue
			}
			dataBytes, _ := json.Marshal(map[string]string{"proactive_message": msg})
			fmt.Fprintf(w, "data: %s\n\n", string(dataBytes))
			flusher.Flush()
		case <-keepAliveTicker.C:
			// Ping SSE sous forme de commentaire (RFC 8895) pour empêcher la coupure par inactivité
			fmt.Fprintf(w, ": keepalive\n\n")
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

	clientDevice := strings.TrimSpace(req.Device)
	if clientDevice == "" {
		// Detect from User-Agent or custom header if omitted
		if strings.Contains(strings.ToLower(r.UserAgent()), "android") || r.Header.Get("X-Pixel-Device") == "android" {
			clientDevice = "android"
		} else {
			clientDevice = "pc"
		}
	}
	if s.superior != nil {
		s.superior.SetActiveClient(clientDevice)
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

	// Ajouter à la mémoire immédiate avec horodatage
	s.stm.AddMessage(llm.Message{Role: llm.RoleUser, Content: req.Message, Timestamp: time.Now()})

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
	var inThinkBlock bool
	var thinkBuf string

	for {
		select {
		case chunk, ok := <-outChan:
			if !ok {
				outChan = nil
			} else {
				if inThinkBlock {
					thinkBuf += chunk
					if idx := strings.Index(thinkBuf, "</think>"); idx != -1 {
						inThinkBlock = false
						remainder := strings.TrimSpace(thinkBuf[idx+len("</think>"):])
						thinkBuf = ""
						if remainder != "" {
							fullReply += remainder
							chunkBytes, _ := json.Marshal(map[string]string{"chunk": remainder})
							fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
							flusher.Flush()
						}
					}
					continue
				}

				if strings.Contains(chunk, "<think>") {
					inThinkBlock = true
					parts := strings.SplitN(chunk, "<think>", 2)
					pre := parts[0]
					thinkBuf = parts[1]
					if pre != "" {
						fullReply += pre
						chunkBytes, _ := json.Marshal(map[string]string{"chunk": pre})
						fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
						flusher.Flush()
					}
					if idx := strings.Index(thinkBuf, "</think>"); idx != -1 {
						inThinkBlock = false
						remainder := strings.TrimSpace(thinkBuf[idx+len("</think>"):])
						thinkBuf = ""
						if remainder != "" {
							fullReply += remainder
							chunkBytes, _ := json.Marshal(map[string]string{"chunk": remainder})
							fmt.Fprintf(w, "data: %s\n\n", string(chunkBytes))
							flusher.Flush()
						}
					}
					continue
				}

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
		s.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: fullReply, Timestamp: time.Now()})
		// Indexation dynamique et tâches de fond lancées uniquement APRÈS l'envoi complet du flux pour éviter toute interférence
		bgCtx := s.sleepManager.AcquireBackgroundContext()

		go func(ctx context.Context) {
			// Attendre une courte pause de 25 secondes de silence avant de lancer les analyses lourdes
			// Si l'utilisateur envoie un nouveau message entre-temps, CancelBackgroundTasks() annulera ce contexte immédiatement.
			select {
			case <-time.After(25 * time.Second):
			case <-ctx.Done():
				return
			}

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

func (s *Server) handleCoreMemoryGoal(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if req.Action == "delete" {
			s.coreMem.RemoveDynamicGoal(req.ID)
		} else {
			status := req.Status
			if status == "" {
				status = "completed"
			}
			s.coreMem.SetDynamicGoalStatus(req.ID, status)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
		return
	} else if r.Method == http.MethodDelete {
		id := r.URL.Query().Get("id")
		if id == "" {
			var req struct {
				ID string `json:"id"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			id = req.ID
		}
		if id != "" {
			s.coreMem.RemoveDynamicGoal(id)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleCoreMemoryVolatile(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete || r.Method == http.MethodPost {
		key := r.URL.Query().Get("key")
		if key == "" {
			var req struct {
				Key string `json:"key"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			key = req.Key
		}
		if key != "" {
			s.coreMem.RemoveVolatileState(key)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status": "ok"}`))
		return
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleCoreMemoryReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.coreMem.Reload()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status": "ok"}`))
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

	isAndroid := strings.Contains(strings.ToLower(r.UserAgent()), "android") ||
		r.Header.Get("X-Pixel-Device") == "android" ||
		r.URL.Query().Get("device") == "android"

	msgs := s.stm.GetMessages()
	filtered := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		if strings.Contains(m.Content, "[Échange autonome avec Gemini]") || strings.Contains(m.Content, "[Échange avec Gemini]") {
			continue
		}
		if isAndroid && isInternalThought(m.Content) {
			continue
		}
		filtered = append(filtered, m)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filtered)
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
	file, header, err := r.FormFile("file")
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur FormFile : %v\n", err)
		http.Error(w, "Impossible de lire le fichier dans le formulaire : "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	ext := ".webm"
	if header != nil && header.Filename != "" {
		if parsedExt := strings.ToLower(filepath.Ext(header.Filename)); parsedExt != "" {
			ext = parsedExt
		}
	}

	// Créer un nom temporaire pour l'enregistrement audio dans /tmp
	tempWebm := filepath.Join(os.TempDir(), fmt.Sprintf("mic_in_%d%s", time.Now().UnixNano(), ext))
	defer os.Remove(tempWebm)
	out, err := os.Create(tempWebm)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur création fichier temp audio : %v\n", err)
		http.Error(w, "Impossible de lire le fichier audio temporaire : "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer out.Close()

	written, err := io.Copy(out, file)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur écriture fichier temp audio : %v\n", err)
		http.Error(w, "Impossible de copier l'audio : "+err.Error(), http.StatusInternalServerError)
		return
	}
	out.Close() // Fermer avant l'appel à ffmpeg
	fmt.Printf("[Server] [Transcription] Audio enregistré dans : %s (taille : %d octets).\n", tempWebm, written)

	// Transcoder en WAV mono 16kHz via ffmpeg
	tempWav := filepath.Join(os.TempDir(), fmt.Sprintf("mic_out_%d.wav", time.Now().UnixNano()))
	defer os.Remove(tempWav)

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
	if text == "" || isInternalThought(text) {
		http.Error(w, "Le paramètre 'text' est requis ou invalide", http.StatusBadRequest)
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

func (s *Server) handleMediaDuck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	duckParam := r.URL.Query().Get("active")
	duck := duckParam == "true" || duckParam == "1"

	targetVol := 18
	if volParam := r.URL.Query().Get("volume"); volParam != "" {
		if v, err := strconv.Atoi(volParam); err == nil && v >= 0 && v <= 100 {
			targetVol = v
		}
	}

	agent.DuckAllMedia(duck, targetVol)
	w.Write([]byte(`{"status": "ok"}`))
}

func (s *Server) handleMediaStreamEnded(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	taskID := r.URL.Query().Get("task_id")
	if taskID == "" && r.Method == http.MethodPost {
		var req struct {
			TaskID string `json:"task_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		taskID = req.TaskID
	}

	if s.scheduler != nil {
		s.scheduler.NotifyStreamDone(taskID)
	}
	w.Write([]byte(`{"status": "ok"}`))
}

func (s *Server) handleMediaStop(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if s.superior != nil {
		s.superior.StopAllMedia()
	} else if s.scheduler != nil {
		s.scheduler.CancelAutoplay()
		s.scheduler.NotifyStreamDone("")
		for _, t := range s.scheduler.GetTasks() {
			if t.Type == "play_music" && (t.Status == scheduler.StatusRunning || t.Status == scheduler.StatusPending) {
				_ = s.scheduler.Cancel(t.ID)
			}
		}
	}
	w.Write([]byte(`{"status": "ok"}`))
}

// handleLocalModels proxies the /v1/models call to the configured local server
// and returns a plain list of model IDs to the frontend.
func (s *Server) handleLocalModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	// Try flm list -j first as fallback for flm backend
	flmBin := "/usr/bin/flm"
	if _, errStat := os.Stat(flmBin); errStat != nil {
		flmBin = "/usr/local/bin/flm"
	}
	out, errCmd := exec.Command(flmBin, "list", "-j").Output()
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

func (s *Server) handleCloudModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	providerParam := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	baseURL := strings.TrimSpace(r.URL.Query().Get("url"))
	apiKey := strings.TrimSpace(r.URL.Query().Get("api_key"))

	settings := s.coreMem.GetLLMSettings()
	if baseURL == "" {
		if providerParam == "onemin" || providerParam == "1min" {
			baseURL = "https://api.1min.ai/openai/v1"
		} else {
			baseURL = settings.CloudBaseURL
		}
	}
	if apiKey == "" {
		apiKey = settings.ResolveAPIKeyForURL(baseURL)
	}

	isOneMin := strings.Contains(strings.ToLower(baseURL), "1min.ai") || providerParam == "onemin" || providerParam == "1min"
	seen := make(map[string]bool)
	models := make([]string, 0)

	// Si clé disponible, interroger en priorité l'API distante /models pour obtenir la liste exacte
	if apiKey != "" && baseURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		remoteModels, err := llm.FetchRemoteModels(ctx, baseURL, apiKey)
		if err == nil {
			for _, m := range remoteModels {
				if !seen[m] {
					seen[m] = true
					models = append(models, m)
				}
			}
		}
	}

	// Compléter avec la liste de référence 1min.AI si l'appel distant n'a rien renvoyé
	if isOneMin && len(models) == 0 {
		for _, m := range llm.DefaultOneMinModels() {
			if !seen[m] {
				seen[m] = true
				models = append(models, m)
			}
		}
	}

	if !isOneMin && len(models) == 0 {
		models = []string{"gemini-3.5-flash", "gemini-3.5-flash-lite", "gemini-2.5-pro", "gemini-2.5-flash"}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"models":  models,
		"has_key": apiKey != "",
	})
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

		// Preserve existing fields if omitted by legacy callers
		prev := s.coreMem.GetLLMSettings()
		if req.OneMinAPIKey == "" {
			req.OneMinAPIKey = prev.OneMinAPIKey
		}
		if req.CloudAPIKey == "" {
			req.CloudAPIKey = prev.CloudAPIKey
		}
		if req.PublishBaseURL == "" {
			req.PublishBaseURL = prev.PublishBaseURL
		}
		if req.PublishModel == "" {
			req.PublishModel = prev.PublishModel
		}
		if req.ReviewBaseURL == "" {
			req.ReviewBaseURL = prev.ReviewBaseURL
		}
		if req.ReviewModel == "" {
			req.ReviewModel = prev.ReviewModel
		}

		s.coreMem.UpdateLLMSettings(req)
		updated := s.coreMem.GetLLMSettings()

		s.adaptiveProvider.UpdateSettings(
			updated.ActiveMode,
			updated.ResolveAPIKeyForURL(updated.CloudBaseURL),
			updated.CloudBaseURL,
			updated.CloudModel,
			updated.LocalBaseURL,
			updated.LocalModel,
		)

		igpuURL := s.igpuBaseURL
		if igpuURL == "" {
			igpuURL = "http://127.0.0.1:18081/v1"
		}
		igpuMod := s.igpuModel
		if igpuMod == "" {
			igpuMod = "qwen3.5:9b"
		}

		if s.publishProvider != nil {
			s.publishProvider.UpdateTaskSettings(
				updated.PublishBaseURL,
				updated.PublishModel,
				updated.ResolveAPIKeyForURL(updated.PublishBaseURL),
				igpuURL,
				igpuMod,
			)
		}
		if s.reviewProvider != nil {
			s.reviewProvider.UpdateTaskSettings(
				updated.ReviewBaseURL,
				updated.ReviewModel,
				updated.ResolveAPIKeyForURL(updated.ReviewBaseURL),
				igpuURL,
				igpuMod,
			)
		}

		if s.superior != nil && s.superior.SkillManager() != nil {
			newCodeProvider := llm.NewTaskAdaptiveProvider(
				updated.CodeBaseURL,
				updated.CodeModel,
				updated.ResolveAPIKeyForURL(updated.CodeBaseURL),
				updated.LocalBaseURL,
				updated.LocalModel,
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
