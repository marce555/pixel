package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// NormalizeDevice normalizes a client device identifier to "android" or "pc".
func NormalizeDevice(device string) string {
	clean := strings.ToLower(strings.TrimSpace(device))
	if clean == "android" || clean == "mobile" || clean == "phone" || clean == "web-mobile" {
		return "android"
	}
	return "pc"
}

func detectClientDevice(r *http.Request, explicit string) string {
	if clean := strings.TrimSpace(explicit); clean != "" {
		return NormalizeDevice(clean)
	}
	if r != nil {
		if headerDev := strings.TrimSpace(r.Header.Get("X-Pixel-Device")); headerDev != "" {
			return NormalizeDevice(headerDev)
		}
		if queryDev := strings.TrimSpace(r.URL.Query().Get("device")); queryDev != "" {
			return NormalizeDevice(queryDev)
		}
		if strings.Contains(strings.ToLower(r.UserAgent()), "android") {
			return "android"
		}
	}
	return "pc"
}

// EventHub manages SSE connections for proactive messages
type EventHub struct {
	clients map[chan string]string // client channel -> normalized device ("pc" or "android")
	mu      sync.Mutex
}

func NewEventHub() *EventHub {
	return &EventHub{
		clients: make(map[chan string]string),
	}
}

func (h *EventHub) AddClient(client chan string) {
	h.AddClientWithDevice(client, "pc")
}

func (h *EventHub) AddClientWithDevice(client chan string, device string) {
	h.mu.Lock()
	h.clients[client] = NormalizeDevice(device)
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

// BroadcastToDevice delivers message exclusively to SSE clients matching targetDevice ("pc" or "android").
// If targetDevice is empty, it falls back to broadcasting to all connected clients.
func (h *EventHub) BroadcastToDevice(device string, message string) {
	if strings.TrimSpace(device) == "" {
		h.Broadcast(message)
		return
	}
	target := NormalizeDevice(device)
	h.mu.Lock()
	defer h.mu.Unlock()
	for client, clientDev := range h.clients {
		if clientDev != target {
			continue
		}
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

func (h *EventHub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Server handles the HTTP API and serves the web frontend.
type Server struct {
	superior         *agent.SuperiorAgent
	coreMem          *memory.CoreMemory
	stm              *memory.STM
	stmMu            sync.Mutex
	androidSTM       *memory.STM
	sleepManager     *agent.SleepManager
	profiler         *agent.ProfilingAgent
	projectAgent     *agent.ProjectAgent
	projectManager   *memory.ProjectManager
	EventHub         *EventHub
	scheduler        *scheduler.Scheduler
	voiceMgr         *voice.VoiceManager
	voiceBridge      *voice.VoiceBridgeManager
	speakerVerifier  *voice.SpeakerVerifier
	adaptiveProvider *llm.AdaptiveProvider
	npuProvider      *llm.AdaptiveProvider
	publishProvider  *llm.AdaptiveProvider
	reviewProvider   *llm.AdaptiveProvider
	igpuBaseURL      string
	igpuModel        string
}

func (s *Server) SetVoiceBridge(vb *voice.VoiceBridgeManager) {
	s.voiceBridge = vb
}

// stmForDevice keeps short-term conversation buffers separate between PC and smartphone
// clients so commands on one device do not hijack router context on the other.
func (s *Server) stmForDevice(device string) *memory.STM {
	if NormalizeDevice(device) == "android" {
		s.stmMu.Lock()
		defer s.stmMu.Unlock()
		if s.stm != nil {
			if s.androidSTM != nil {
				s.stm.SetDeviceSTM("android", s.androidSTM)
			}
			s.androidSTM = s.stm.ForDevice("android")
			return s.androidSTM
		}
		if s.androidSTM == nil {
			s.androidSTM = memory.NewSTM(12)
		}
		return s.androidSTM
	}
	return s.stm
}

func (s *Server) getVoiceBridge() *voice.VoiceBridgeManager {
	if s.voiceBridge != nil {
		return s.voiceBridge
	}
	if s.superior != nil {
		return s.superior.GetVoiceBridge()
	}
	return nil
}

func (s *Server) SetSpeakerVerifier(sv *voice.SpeakerVerifier) {
	s.speakerVerifier = sv
}

func (s *Server) SetNPUProvider(npu *llm.AdaptiveProvider) {
	s.npuProvider = npu
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
	mux.HandleFunc("/api/vision_settings", s.handleVisionSettings)
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
	mux.HandleFunc("/api/voice_bridge_settings", s.handleVoiceBridgeSettings)
	mux.HandleFunc("/api/sounds/bridge", s.handleBridgeSound)
	mux.HandleFunc("/api/voice/speaker/profile", s.handleVoiceSpeakerProfile)
	mux.HandleFunc("/api/voice/speaker/enroll", s.handleVoiceSpeakerEnroll)

	// Media Control Routes (Audio Ducking, Streaming)
	mux.HandleFunc("/api/media/duck", s.handleMediaDuck)
	mux.HandleFunc("/api/media/stream_ended", s.handleMediaStreamEnded)
	mux.HandleFunc("/api/media/stop", s.handleMediaStop)
	mux.HandleFunc("/api/media/mix", s.handleMediaMix)
	mux.HandleFunc("/api/media/live_radio", s.handleLiveRadioMix)

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
		// Autoriser le téléchargement direct de l'APK et les flux mix signés par jeton de session
		if r.URL.Path == "/api/apk" || r.URL.Path == "/api/media/mix" || r.URL.Path == "/api/media/live_radio" {
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

	clientDevice := detectClientDevice(r, "")
	isAndroid := clientDevice == "android"

	clientChan := make(chan string, 100)
	s.EventHub.AddClientWithDevice(clientChan, clientDevice)
	fmt.Printf("[Server] [SSE] Client connecté (device=%s, android=%v, total=%d, remote=%s)\n", clientDevice, isAndroid, s.EventHub.ClientCount(), r.RemoteAddr)

	defer func() {
		s.EventHub.RemoveClient(clientChan)
		close(clientChan)
		fmt.Printf("[Server] [SSE] Client déconnecté (android=%v, restants=%d, remote=%s)\n", isAndroid, s.EventHub.ClientCount(), r.RemoteAddr)
	}()

	// Battement de cœur SSE toutes les 15s pour maintenir active la table NAT Freebox et le réseau mobile
	keepAliveTicker := time.NewTicker(15 * time.Second)
	defer keepAliveTicker.Stop()

	// Si le profil actif est actuellement Inconnu et la caméra est activée, lancer un scan d'identification visuelle d'ouverture
	if s.superior.GetVisionAgent() != nil && s.coreMem.IsCameraEnabled() && s.coreMem.GetProfile().Static.Name == "Inconnu" {
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

	clientDevice := detectClientDevice(r, req.Device)
	if s.superior != nil {
		s.superior.SetActiveClient(clientDevice)
	}
	reqCtx := agent.WithClientDevice(r.Context(), clientDevice)
	reqCtx = agent.WithRequestBaseURL(reqCtx, requestBaseURL(r))

	// Prioriser immédiatement le chat utilisateur en annulant les tâches de fond en cours (libère la GPU)
	s.sleepManager.CancelBackgroundTasks()

	activeSTM := s.stmForDevice(clientDevice)

	// Si la mémoire STM est saturée, sauvegarder les messages pour les consolider après la génération de la réponse
	var msgsToConsolidate []llm.Message
	if activeSTM.IsFull() {
		fmt.Println("[Server] STM saturée avant nouveau message. La consolidation d'urgence sera lancée après la réponse...")
		msgs := activeSTM.GetMessages()
		// Consolider la première moitié des messages (les plus anciens)
		// et garder la seconde moitié pour préserver le contexte de la conversation.
		half := len(msgs) / 2
		msgsToConsolidate = msgs[:half]
		msgsToKeep := msgs[half:]

		activeSTM.Clear()
		for _, msg := range msgsToKeep {
			activeSTM.AddMessage(msg)
		}
	}

	// Ajouter à la mémoire immédiate avec horodatage
	activeSTM.AddMessage(llm.Message{Role: llm.RoleUser, Content: req.Message, Timestamp: time.Now()})

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

	outChan, bridgeChan, errChan := s.superior.ProcessInputStreamWithBridge(reqCtx, req.Message, activeSTM.GetMessages())

	var fullReply string
	var inThinkBlock bool
	var thinkBuf string

	for {
		select {
		case evt, ok := <-bridgeChan:
			if !ok {
				bridgeChan = nil
			} else if evt.Phrase != "" {
				bridgeBytes, _ := json.Marshal(map[string]interface{}{
					"bridge_alert": true,
					"intent":       string(evt.Intent),
					"is_micro":     evt.IsMicro,
					"sound_url":    "/api/sounds/bridge",
				})
				fmt.Fprintf(w, "data: %s\n\n", string(bridgeBytes))
				flusher.Flush()
			}
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

		if outChan == nil && bridgeChan == nil && errChan == nil {
			break
		}
	}

	if fullReply != "" {
		activeSTM.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: fullReply, Timestamp: time.Now()})
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

	clientDevice := detectClientDevice(r, "")
	isAndroid := clientDevice == "android"

	activeSTM := s.stmForDevice(clientDevice)
	var msgs []llm.Message
	if activeSTM != nil {
		msgs = activeSTM.GetMessages()
	}
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

// saveUploadedAudioAsWav reads the uploaded multipart "file" and normalizes it into a 16kHz mono PCM WAV file.
func (s *Server) saveUploadedAudioAsWav(r *http.Request) (wavPath string, cleanup func(), statusCode int, err error) {
	if s.voiceMgr == nil {
		return "", nil, http.StatusInternalServerError, fmt.Errorf("Le module vocal local n'est pas initialisé")
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return "", nil, http.StatusBadRequest, fmt.Errorf("Impossible de lire le fichier dans le formulaire : %w", err)
	}
	defer file.Close()

	ext := ".webm"
	if header != nil && header.Filename != "" {
		if parsedExt := strings.ToLower(filepath.Ext(header.Filename)); parsedExt != "" {
			ext = parsedExt
		}
	}

	tempIn := filepath.Join(os.TempDir(), fmt.Sprintf("mic_in_%d%s", time.Now().UnixNano(), ext))
	defer os.Remove(tempIn)

	out, err := os.Create(tempIn)
	if err != nil {
		return "", nil, http.StatusInternalServerError, fmt.Errorf("Impossible de lire le fichier audio temporaire : %w", err)
	}

	written, err := io.Copy(out, file)
	if err != nil {
		out.Close()
		return "", nil, http.StatusInternalServerError, fmt.Errorf("Impossible de copier l'audio : %w", err)
	}
	out.Close()
	fmt.Printf("[Server] [Transcription] Audio enregistré dans : %s (taille : %d octets).\n", tempIn, written)

	tempWav := filepath.Join(os.TempDir(), fmt.Sprintf("mic_out_%d.wav", time.Now().UnixNano()))
	if err := s.voiceMgr.ConvertWebMToWav(tempIn, tempWav); err != nil {
		os.Remove(tempWav)
		return "", nil, http.StatusInternalServerError, fmt.Errorf("Échec du décodage audio via ffmpeg : %w", err)
	}
	return tempWav, func() { _ = os.Remove(tempWav) }, http.StatusOK, nil
}

func (s *Server) handleVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fmt.Println("[Server] [Transcription] Requête de transcription reçue.")
	tempWav, cleanup, statusCode, err := s.saveUploadedAudioAsWav(r)
	if err != nil {
		fmt.Printf("[Server] [Transcription] Erreur audio : %v\n", err)
		http.Error(w, err.Error(), statusCode)
		return
	}
	defer cleanup()
	fmt.Println("[Server] [Transcription] Décryptage et transcodage ffmpeg réussis.")

	// Run Whisper STT and ONNX speaker verification concurrently over the normalized 16kHz WAV.
	var (
		text      string
		whisperEr error
		match     voice.SpeakerMatchResult
		wg        sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		text, whisperEr = s.voiceMgr.TranscribeAudio(r.Context(), tempWav)
	}()

	if s.speakerVerifier != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			match = s.speakerVerifier.VerifyWav(r.Context(), tempWav)
		}()
	}
	wg.Wait()

	if whisperEr != nil {
		fmt.Printf("[Server] [Transcription] Erreur Whisper : %v\n", whisperEr)
		http.Error(w, "Échec de la transcription Whisper locale : "+whisperEr.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Printf("[Server] [Transcription] Résultat Whisper : '%s'\n", text)

	handsFreeNoWake := false
	var decision voice.AddressDecision
	if s.speakerVerifier != nil {
		s.speakerVerifier.RecordObservation(match, text)
		handsFreeNoWake = s.speakerVerifier.IsHandsFreeNoWake()
		var lastAct time.Time
		clientDevice := detectClientDevice(r, "")
		if devSTM := s.stmForDevice(clientDevice); devSTM != nil {
			lastAct = devSTM.GetLastActivity()
		}
		var cls voice.AddressClassifier
		if s.npuProvider != nil {
			cls = &npuAddressClassifier{provider: s.npuProvider}
		}
		decision = voice.EvaluateSpokenTurn(r.Context(), text, match, handsFreeNoWake, lastAct, cls)
		fmt.Printf("[Server] [Speaker] mode=%s no_wake=%v owner=%v score=%.3f (centroid=%.3f best=%.3f dur=%.0fms infer=%.1fms) accept=%v intent=%s reason=%s\n",
			match.Mode, handsFreeNoWake, match.IsOwner, match.Score, match.CentroidScore, match.BestSampleScore, match.DurationMs, match.InferMs, decision.ShouldAccept, decision.Intent, match.Reason)
	}

	resp := map[string]any{
		"text":              text,
		"speaker_available": match.Available,
		"speaker_enrolled":  match.Enrolled,
		"speaker_score":     match.Score,
		"is_owner":          match.IsOwner,
		"speaker_mode":      match.Mode,
		"speaker_reason":    match.Reason,
		"speaker_duration":  match.DurationMs,
		"handsfree_no_wake": handsFreeNoWake,
		"should_accept":     decision.ShouldAccept,
		"address_intent":    decision.Intent,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type npuAddressClassifier struct {
	provider *llm.AdaptiveProvider
}

func (c *npuAddressClassifier) ClassifyAddressedToPixel(ctx context.Context, utterance string) (bool, error) {
	if c == nil || c.provider == nil {
		return false, fmt.Errorf("npu provider not configured")
	}
	msgs := []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: "Tu es un filtre d'écoute vocale. Détermine si l'utilisateur s'adresse à son assistant vocal (Pixel) ou parle à une autre personne / au téléphone / tout seul. Réponds STRICTEMENT par un seul mot : ASSISTANT ou TIERS.",
		},
		{
			Role:    llm.RoleUser,
			Content: `Phrase entendue : "` + utterance + `"`,
		},
	}
	raw, err := c.provider.GenerateWithMaxTokens(ctx, msgs, 12)
	if err != nil {
		return false, err
	}
	upper := strings.ToUpper(strings.TrimSpace(raw))
	if strings.Contains(upper, "ASSISTANT") {
		return true, nil
	}
	if strings.Contains(upper, "TIERS") {
		return false, nil
	}
	return false, fmt.Errorf("unexpected classifier output: %q", raw)
}

func (s *Server) handleVoiceSpeakerProfile(w http.ResponseWriter, r *http.Request) {
	if s.speakerVerifier == nil {
		http.Error(w, "Vérificateur vocal non initialisé", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.speakerVerifier.GetStatus(r.Context()))
	case http.MethodPost:
		var req struct {
			Enabled         *bool    `json:"enabled"`
			ObserveOnly     *bool    `json:"observe_only"`
			HandsFreeNoWake *bool    `json:"handsfree_no_wake"`
			Threshold       *float64 `json:"threshold"`
			OwnerName       *string  `json:"owner_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.speakerVerifier.UpdateSettings(req.Enabled, req.ObserveOnly, req.HandsFreeNoWake, req.Threshold, req.OwnerName); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.speakerVerifier.GetStatus(r.Context()))
	case http.MethodDelete:
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if err := s.speakerVerifier.DeleteSample(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.speakerVerifier.GetStatus(r.Context()))
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVoiceSpeakerEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.speakerVerifier == nil {
		http.Error(w, "Vérificateur vocal non initialisé", http.StatusServiceUnavailable)
		return
	}

	tempWav, cleanup, statusCode, err := s.saveUploadedAudioAsWav(r)
	if err != nil {
		http.Error(w, err.Error(), statusCode)
		return
	}
	defer cleanup()

	device := r.FormValue("device")
	phrase := r.FormValue("phrase")
	res, err := s.speakerVerifier.EnrollWav(r.Context(), tempWav, device, phrase)
	if err != nil {
		if errors.Is(err, voice.ErrAudioTooShort) {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		if errors.Is(err, voice.ErrSpeakerWorkerUnavailable) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
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

	// Servir immédiatement depuis le pré-cache audio si disponible (latence < 1ms)
	if vb := s.getVoiceBridge(); vb != nil {
		if cachedAudio, ok := vb.GetCachedAudio(text); ok {
			w.Header().Set("Content-Type", "audio/wav")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(cachedAudio)))
			w.Write(cachedAudio)
			return
		}
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

var (
	bridgeEarconOnce sync.Once
	bridgeEarconWAV  []byte
)

func getBridgeEarconWAV() []byte {
	bridgeEarconOnce.Do(func() {
		bridgeEarconWAV = voice.BuildThinkingAlertWAV()
	})
	return bridgeEarconWAV
}

func (s *Server) handleBridgeSound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	wav := getBridgeEarconWAV()
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(wav)))
	w.Write(wav)
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

var (
	reMixSessionID = regexp.MustCompile(`^[a-fA-F0-9]{8,32}$`)
	reMixChunkFile = regexp.MustCompile(`^part_\d{3}\.(mp3|flac)$`)
)

func requestBaseURL(r *http.Request) string {
	if r == nil || strings.TrimSpace(r.Host) == "" {
		return "http://127.0.0.1:8080"
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func validateMixSessionDir(w http.ResponseWriter, r *http.Request) (string, bool) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	sig := strings.TrimSpace(r.URL.Query().Get("sig"))
	if !reMixSessionID.MatchString(sessionID) || sig == "" {
		http.Error(w, "Paramètres de mix invalides", http.StatusBadRequest)
		return "", false
	}

	sessionDir := filepath.Join("/tmp/pixel_openaudio_mix", sessionID)
	tokenBytes, err := os.ReadFile(filepath.Join(sessionDir, "mix_token.txt"))
	if err != nil {
		http.Error(w, "Session mix introuvable", http.StatusNotFound)
		return "", false
	}
	expectedSig := strings.TrimSpace(string(tokenBytes))
	if expectedSig == "" || subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		http.Error(w, "Non autorisé", http.StatusUnauthorized)
		return "", false
	}
	return sessionDir, true
}

func (s *Server) handleMediaMix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fileName := strings.TrimSpace(r.URL.Query().Get("file"))
	if !reMixChunkFile.MatchString(fileName) {
		http.Error(w, "Paramètres de mix invalides", http.StatusBadRequest)
		return
	}
	sessionDir, ok := validateMixSessionDir(w, r)
	if !ok {
		return
	}

	targetPath := filepath.Join(sessionDir, fileName)
	if _, err := os.Stat(targetPath); err != nil {
		http.Error(w, "Segment mix introuvable", http.StatusNotFound)
		return
	}
	if strings.HasSuffix(fileName, ".mp3") {
		w.Header().Set("Content-Type", "audio/mpeg")
	} else {
		w.Header().Set("Content-Type", "audio/flac")
	}
	http.ServeFile(w, r, targetPath)
}

func findMixChunkReady(sessionDir string, idx int) (string, bool) {
	for _, ext := range []string{"flac", "mp3", "wav"} {
		cand := filepath.Join(sessionDir, fmt.Sprintf("part_%03d.%s", idx, ext))
		if _, errReady := os.Stat(cand + ".ready"); errReady == nil {
			if _, errFile := os.Stat(cand); errFile == nil {
				return cand, true
			}
		}
	}
	return "", false
}

// handleLiveRadioMix streams all OpenAudio DJ mix segments through a single persistent
// MP3 encoder pipe so mobile clients receive an unbroken gapless audio/mpeg radio stream.
func (s *Server) handleLiveRadioMix(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionDir, ok := validateMixSessionDir(w, r)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmdEncode := exec.CommandContext(ctx,
		"ffmpeg", "-v", "error",
		"-f", "s16le", "-ar", "44100", "-ac", "2", "-i", "pipe:0",
		"-c:a", "libmp3lame", "-b:a", "256k", "-flush_packets", "1",
		"-f", "mp3", "pipe:1",
	)
	encodeStdin, errIn := cmdEncode.StdinPipe()
	if errIn != nil {
		http.Error(w, "Erreur encodeur audio", http.StatusInternalServerError)
		return
	}
	encodeStdout, errOut := cmdEncode.StdoutPipe()
	if errOut != nil {
		http.Error(w, "Erreur sortie audio", http.StatusInternalServerError)
		return
	}
	if errStart := cmdEncode.Start(); errStart != nil {
		http.Error(w, "Impossible de démarrer ffmpeg", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		buf := make([]byte, 16384)
		for {
			n, errRead := encodeStdout.Read(buf)
			if n > 0 {
				if _, errWrite := w.Write(buf[:n]); errWrite != nil {
					cancel()
					return
				}
				if canFlush {
					flusher.Flush()
				}
			}
			if errRead != nil {
				return
			}
		}
	}()

	stopFile := filepath.Join(sessionDir, "stop.txt")
	doneFile := filepath.Join(sessionDir, "done.txt")
	consumedTmp := filepath.Join(sessionDir, "consumed_pos.txt.tmp")
	consumedFile := filepath.Join(sessionDir, "consumed_pos.txt")

	for idx := 0; ; idx++ {
		if ctx.Err() != nil {
			break
		}
		if _, errStop := os.Stat(stopFile); errStop == nil {
			break
		}

		chunkPath := ""
		waitDeadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(waitDeadline) {
			if ctx.Err() != nil {
				break
			}
			if _, errStop := os.Stat(stopFile); errStop == nil {
				break
			}
			if foundPath, ready := findMixChunkReady(sessionDir, idx); ready {
				chunkPath = foundPath
				break
			}
			if _, errDone := os.Stat(doneFile); errDone == nil {
				if foundPath, ready := findMixChunkReady(sessionDir, idx); ready {
					chunkPath = foundPath
				}
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(150 * time.Millisecond):
			}
		}
		if chunkPath == "" {
			break
		}

		if errWritePos := os.WriteFile(consumedTmp, []byte(fmt.Sprintf("%d", idx)), 0644); errWritePos == nil {
			_ = os.Rename(consumedTmp, consumedFile)
		}

		cmdDec := exec.CommandContext(ctx,
			"ffmpeg", "-v", "error", "-i", chunkPath,
			"-f", "s16le", "-ar", "44100", "-ac", "2", "pipe:1",
		)
		decStdout, errDecPipe := cmdDec.StdoutPipe()
		if errDecPipe != nil {
			break
		}
		if errDecStart := cmdDec.Start(); errDecStart != nil {
			break
		}
		_, errCopy := io.Copy(encodeStdin, decStdout)
		_ = cmdDec.Wait()
		if errCopy != nil || ctx.Err() != nil {
			break
		}
	}

	_ = encodeStdin.Close()
	<-pumpDone
	_ = cmdEncode.Wait()
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

func (s *Server) handleVisionSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(memory.VisionSettings{
			CameraEnabled: s.coreMem.IsCameraEnabled(),
		})
		return
	}

	if r.Method == http.MethodPost {
		var req memory.VisionSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		s.coreMem.SetCameraEnabled(req.CameraEnabled)

		statusMsg := "Caméra activée, en attente du prochain scan."
		if !req.CameraEnabled {
			statusMsg = "Caméra éteinte (désactivée dans la configuration)."
		}
		if s.EventHub != nil {
			s.EventHub.Broadcast("VISION:" + statusMsg)
		}

		// Si l'utilisateur vient de rallumer la caméra, déclencher un scan asynchrone si VisionAgent est disponible
		if req.CameraEnabled && s.superior != nil && s.superior.GetVisionAgent() != nil {
			go func() {
				s.superior.GetVisionAgent().ScanOnce(context.Background())
			}()
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":         "ok",
			"camera_enabled": req.CameraEnabled,
		})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleVoiceBridgeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		if s.coreMem != nil {
			json.NewEncoder(w).Encode(s.coreMem.GetVoiceBridgeSettings())
			return
		}
		enabled := false
		if vb := s.getVoiceBridge(); vb != nil {
			enabled = vb.IsEnabled()
		}
		json.NewEncoder(w).Encode(memory.VoiceBridgeSettings{Enabled: enabled, ThresholdMs: 550, AdaptiveExtraMs: 350, PreCacheAudio: true})
		return
	}

	if r.Method == http.MethodPost {
		var req memory.VoiceBridgeSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if s.coreMem != nil {
			s.coreMem.UpdateVoiceBridgeSettings(req)
			req = s.coreMem.GetVoiceBridgeSettings()
		}
		if vb := s.getVoiceBridge(); vb != nil {
			vb.UpdateConfig(req.Enabled, req.ThresholdMs, req.AdaptiveExtraMs, req.PreCacheAudio)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "ok",
			"settings": req,
		})
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}
