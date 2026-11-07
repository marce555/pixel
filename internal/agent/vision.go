package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type VisionAgent struct {
	provider               llm.Provider
	coreMemory             *memory.CoreMemory
	ltm                    *memory.LTM
	stm                    *memory.STM
	thoughtStream          *memory.ThoughtStream
	onBroadcast            func(message string)
	client                 *http.Client
	marceloSeenThisSession bool
	firstScanDone          bool
	lastCuriousComment     time.Time
	lastSurveillanceAlert  time.Time
	cameraMu               sync.Mutex
	stateMu                sync.Mutex
	lastManualScan         time.Time
}

func NewVisionAgent(provider llm.Provider, coreMemory *memory.CoreMemory, ltm *memory.LTM, stm *memory.STM, thoughtStream *memory.ThoughtStream, onBroadcast func(message string)) *VisionAgent {
	return &VisionAgent{
		provider:               provider,
		coreMemory:             coreMemory,
		ltm:                    ltm,
		stm:                    stm,
		thoughtStream:          thoughtStream,
		onBroadcast:            onBroadcast,
		client:                 &http.Client{Timeout: 45 * time.Second},
		marceloSeenThisSession: false,
		firstScanDone:          false,
		lastCuriousComment:     time.Now().Add(-5 * time.Minute), // Allow immediate trigger after initial greeting
	}
}

// Start launches the background goroutine that captures and analyzes the environment every 5 minutes.
func (va *VisionAgent) Start(ctx context.Context) {
	log.Println("[VisionAgent] Démarrage du module de vision local Go (taux: 5m, avec veille dynamique)...")
	go func() {
		// Wait 5 seconds for the rest of the application to initialize
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}

		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// Wait a few seconds to let other components start up and check if chat is active
		time.Sleep(5 * time.Second)
		lastActStart := va.stm.GetLastActivity()
		if time.Since(lastActStart) < 2*time.Minute {
			log.Println("[VisionAgent] Premier scan ignoré car une conversation est déjà active au démarrage.")
			va.firstScanDone = true
		} else {
			log.Println("[VisionAgent] Premier scan de démarrage...")
			description, err := va.CaptureAndAnalyze(ctx)
			if err == nil && description != "" {
				va.ProcessVisualPerception(ctx, description, true)
				va.firstScanDone = true
			} else if err != nil {
				fmt.Printf("[VisionAgent] Premier scan échoué: %v\n", err)
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Avoid system overload by checking dynamic suspension conditions

				// 1. Suspension if active chat is in progress (preserve NPU loaded model for 5 minutes of inactivity)
				lastAct := va.stm.GetLastActivity()
				if time.Since(lastAct) < 5*time.Minute {
					log.Println("[VisionAgent] Veille active : conversation en cours (activité récente < 5m). GPU/NPU préservé pour le chat.")
					continue
				}

				// 2. Suspension if Pixel is asleep, consolidating, user is resting, or during night hours (23h-7h)
				profile := va.coreMemory.GetProfile()
				avail := profile.Volatile["Disponibilité"]
				humeur := strings.ToLower(profile.Volatile["Humeur"])

				isSleepTime := false
				hour := time.Now().Hour()
				if hour >= 23 || hour < 7 {
					isSleepTime = true
				}

				if avail == "En sommeil" || avail == "Consolidation..." || avail == "Inaccessible" ||
					strings.Contains(humeur, "sommeil") || strings.Contains(humeur, "dormir") || strings.Contains(humeur, "repos") ||
					isSleepTime {
					log.Println("[VisionAgent] Veille active : Pixel est en consolidation, en sommeil, au repos, indisponible ou en période nocturne.")
					continue
				}

				// Otherwise, proceed to scan
				description, err := va.CaptureAndAnalyze(ctx)
				if err != nil {
					fmt.Printf("[VisionAgent] Erreur de vision: %v\n", err)
					continue
				}

				if description != "" {
					va.ProcessVisualPerception(ctx, description, true)
					va.firstScanDone = true
				}
			}
		}
	}()
}

// CaptureAndAnalyze captures a frame from /dev/video0 using ffmpeg and sends it to FastFlowLM.
func (va *VisionAgent) CaptureAndAnalyze(ctx context.Context) (string, error) {
	va.cameraMu.Lock()
	defer va.cameraMu.Unlock()

	tempFile := fmt.Sprintf("webcam_temp_%d.jpg", time.Now().UnixNano())
	defer os.Remove(tempFile) // Ensure cleanup

	// 1. Capture a single frame from webcam via ffmpeg (standard video4linux2 device)
	// -y : overwrite
	// -f video4linux2 : device format
	// -i /dev/video0 : input webcam
	// -vframes 1 : capture 1 frame
	// -video_size 640x480 : resolution (perfect compromise for fast local inference)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-f", "video4linux2", "-i", "/dev/video0", "-vframes", "1", "-video_size", "640x480", tempFile)

	// Capture stderr diagnostics
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("capture webcam via ffmpeg échouée: %w (details: %s)", err, stderr.String())
	}

	// 2. Read the image file and check pixel-level luminance before calling the vision model
	imgBytes, err := os.ReadFile(tempFile)
	if err != nil {
		return "", fmt.Errorf("lecture de l'image capturée échouée: %w", err)
	}

	if isJPEGImageDarkOrBlack(imgBytes) {
		log.Println("[VisionAgent] Image capturée noire ou trop sombre (luminance insuffisante) : aucune inférence lancée.")
		return "L'image est entièrement noire et ne montre aucune personne, ni aucun détail visible.", nil
	}

	imgBase64 := base64.StdEncoding.EncodeToString(imgBytes)
	imgDataURL := "data:image/jpeg;base64," + imgBase64

	// 3. Prepare the multimodal OpenAI Vision JSON request payload
	flmURL := "http://127.0.0.1:52625/v1/chat/completions"

	type visionMessageContent struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url,omitempty"`
	}

	type visionMessage struct {
		Role    string                 `json:"role"`
		Content []visionMessageContent `json:"content"`
	}

	type visionRequest struct {
		Model     string          `json:"model"`
		Messages  []visionMessage `json:"messages"`
		MaxTokens int             `json:"max_tokens"`
	}

	reqBody := visionRequest{
		Model:     "qwen3vl-it:4b", // target the vision model pulled by Marcelo
		MaxTokens: 128,
		Messages: []visionMessage{
			{
				Role: "user",
				Content: []visionMessageContent{
					{
						Type: "text",
						Text: "Décris brièvement en une seule phrase simple ce que tu vois devant la caméra. RÈGLE STRICTE : Si l'image est noire, sombre, mal éclairée, floue, ou si aucune personne n'est clairement et nettement visible en pleine lumière, réponds UNIQUEMENT : 'L'image est noire et aucune personne n'est visible.' N'invente JAMAIS de silhouette ni de personne dans l'obscurité. Si une personne est nettement visible, décris brièvement son apparence physique et ce qu'elle fait. Réponds en français.",
					},
					{
						Type: "image_url",
					},
				},
			},
		},
	}
	reqBody.Messages[0].Content[1].ImageURL.URL = imgDataURL

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("encodage JSON de la requête vision échoué: %w", err)
	}

	// 4. Send request to FastFlowLM (FLM)
	req, err := http.NewRequestWithContext(ctx, "POST", flmURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("création de la requête HTTP vision échouée: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := va.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("appel à l'API FastFlowLM vision échoué: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("erreur FastFlowLM vision %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// 5. Parse response
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `choices`
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("parsing réponse vision échoué: %w (raw: %s)", err, string(bodyBytes))
	}

	if len(chatResp.Choices) > 0 {
		return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
	}

	return "", fmt.Errorf("aucune description visuelle renvoyée par le modèle (raw: %s)", string(bodyBytes))
}

// isJPEGImageDarkOrBlack decodes a JPEG image and returns true if the frame is black or too dark
// to reliably distinguish any human face (preventing VLM hallucinations in the dark).
func isJPEGImageDarkOrBlack(imgBytes []byte) bool {
	if len(imgBytes) == 0 {
		return true
	}
	img, err := jpeg.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return false
	}
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w <= 0 || h <= 0 {
		return true
	}

	var totalLuma float64
	var brightCount int
	var sampleCount int
	step := 4
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			// Convert 16-bit RGBA to 8-bit luma (0..255)
			luma := (0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8))
			totalLuma += luma
			if luma >= 85.0 {
				brightCount++
			}
			sampleCount++
		}
	}
	if sampleCount == 0 {
		return true
	}
	meanLuma := totalLuma / float64(sampleCount)
	brightRatio := float64(brightCount) / float64(sampleCount)

	// If average luminance is very low (< 48/255) or almost no pixels are well-lit (< 3% above 85/255),
	// the room/screen is dark or the camera is obstructed.
	return meanLuma < 48.0 || (meanLuma < 62.0 && brightRatio < 0.03)
}

// IsEmptyOrBlackVision detects black screens, dark/obstructed camera frames, indistinct silhouettes, or scenes with nobody present.
func IsEmptyOrBlackVision(description string) bool {
	d := strings.ToLower(strings.TrimSpace(description))
	if d == "" {
		return true
	}
	emptyOrBlackMarkers := []string{
		"image complètement noire", "image noire", "image entièrement noire",
		"presque noire", "quasiment noire", "écran noir", "ecran noir",
		"tout noir", "fond noir", "entièrement noir", "totalement noir",
		"environnement est entièrement noir", "dans le noir",
		"très sombre", "trop sombre", "image sombre", "salle sombre",
		"pièce sombre", "pièce très sombre", "cuisine sombre", "scène sombre",
		"scène intérieure sombre", "intérieur sombre", "environnement sombre",
		"chambre sombre", "bureau sombre", "coin sombre", "endroit sombre",
		"fond sombre", "cadre sombre", "vue sombre", "photo sombre", "capture sombre",
		"obscurité", "pénombre", "éclairage faible", "faible éclairage",
		"faiblement éclairé", "peu éclairé", "peu éclairée", "mal éclairé", "mal éclairée",
		"luminosité faible", "faible luminosité", "lumière faible",
		"aucun détail visible", "aucun détail discernable", "aucun détail ne peut",
		"ne vois rien", "rien n'est visible", "difficile à discerner", "difficilement discernable",
		"objectif obstrué", "caméra masquée", "caméra cachée",
		"image floue", "légèrement floue", "image grainée", "formes floues", "forme floue",
		"silhouette", "indistinct", "partiellement visible",
		"sans personne", "sans aucune personne", "aucune personne", "aucun visage", "aucun individu",
		"personne n'est", "il n'y a personne", "ne vois personne", "ne vois clairement personne",
		"pas de personne", "pas de visage", "aucune présence", "non, aucune",
		"pièce vide", "chambre vide", "bureau vide", "fauteuil vide", "chaise vide",
		"salle vide", "mur vide", "seulement un plafond", "absent", "parti",
	}
	for _, m := range emptyOrBlackMarkers {
		if strings.Contains(d, m) {
			return true
		}
	}
	return false
}

func hasHumanPresence(description string) bool {
	if IsEmptyOrBlackVision(description) {
		return false
	}
	d := strings.ToLower(description)
	humanMarkers := []string{
		"homme", "femme", "personne", "visage", "individu", "garçon", "fille",
		"monsieur", "dame", "marcelo", "assis", "debout", "lunettes", "barbe", "barbu",
	}
	for _, m := range humanMarkers {
		if strings.Contains(d, m) {
			return true
		}
	}
	return false
}

func isMarceloNegated(descLower string) bool {
	negations := []string{
		"sans marcelo", "pas même marcelo", "pas marcelo", "ni marcelo",
		"aucun marcelo", "marcelo n'est pas", "absence de marcelo",
	}
	for _, n := range negations {
		if strings.Contains(descLower, n) {
			return true
		}
	}
	return false
}

func hasMultiplePeople(descLower string) bool {
	multi := []string{
		"deux personnes", "trois personnes", "plusieurs personnes",
		"une autre personne", "accompagné", "accompagnée", "deux hommes", "deux femmes",
	}
	for _, m := range multi {
		if strings.Contains(descLower, m) {
			return true
		}
	}
	return false
}

func isLikelyMarcelo(descLower string) bool {
	if isMarceloNegated(descLower) || hasMultiplePeople(descLower) {
		return false
	}
	if strings.Contains(descLower, "marcelo") {
		return true
	}
	if strings.Contains(descLower, "jeune homme") || strings.Contains(descLower, "adolescent") ||
		strings.Contains(descLower, "enfant") || strings.Contains(descLower, "femme") ||
		strings.Contains(descLower, "fille") || strings.Contains(descLower, "dame") {
		return false
	}
	isMaleOrPerson := strings.Contains(descLower, "homme") || strings.Contains(descLower, "monsieur") || strings.Contains(descLower, "personne")
	if !isMaleOrPerson {
		return false
	}
	marceloTraits := []string{
		"cheveux gris", "cheveux blancs", "poivre et sel", "homme âgé", "âge mûr",
		"d'âge mûr", "portant des lunettes", "avec des lunettes",
	}
	for _, t := range marceloTraits {
		if strings.Contains(descLower, t) {
			return true
		}
	}
	return false
}

// ProcessVisualPerception updates CoreMemory volatile state, indexes memory in LTM, and acts as a surveillance system.
// Black screens, empty scenes, or scenes with only Marcelo NEVER trigger notifications.
// Only the presence of another person (unknown or someone other than Marcelo) triggers a surveillance notification.
func (va *VisionAgent) ProcessVisualPerception(ctx context.Context, description string, isBackground bool) {
	fmt.Printf("[VisionAgent] Perception Visuelle (isBackground=%t) : %s\n", isBackground, description)

	// 1. Broadcast raw status to PC Web UI ONLY (filtered out on Android via VISION: prefix)
	va.onBroadcast("VISION:" + description)

	// 2. Filter out black screens, dark frames, or empty scenes (nobody present)
	if IsEmptyOrBlackVision(description) || !hasHumanPresence(description) {
		va.coreMemory.UpdateVolatileState("Dernière vision", description)
		if va.thoughtStream != nil {
			va.thoughtStream.AddThought("[Vision] "+description, nil)
		}
		log.Println("[VisionAgent] Mode surveillance : écran noir ou aucune personne présente (aucune notification envoyée).")
		return
	}

	// 3. Identify who is in front of the camera BEFORE overwriting "Dernière vision"
	descLower := strings.ToLower(description)
	presentPerson := ""

	if hasMultiplePeople(descLower) {
		presentPerson = "Inconnu"
	} else if isLikelyMarcelo(descLower) {
		presentPerson = "Marcelo"
	} else {
		presentPerson = va.identifyInterlocutor(ctx, description)
	}

	// 4. Update Core Memory (Volatile) and index valid human visual perception in LTM
	va.coreMemory.UpdateVolatileState("Dernière vision", description)

	embedding, err := va.provider.CreateEmbedding(ctx, description)
	if err == nil && len(embedding) > 0 {
		va.ltm.StoreMemory(
			ctx,
			"Personal",
			"vision",
			"Vision : "+time.Now().Format("15:04"),
			"Pixel a vu dans son environnement : "+description,
			[]string{"vision", "camera", "perception", "environnement"},
			embedding,
			0.65,
		)
	}

	if va.thoughtStream != nil {
		va.thoughtStream.AddThought("[Vision] "+description, embedding)
		log.Println("[VisionAgent] Observation visuelle enregistrée dans le forum intérieur (ThoughtStream).")
	}

	fmt.Printf("[VisionAgent] Personne identifiée face à la caméra : %s\n", presentPerson)

	currentActive := va.coreMemory.GetProfile().Static.Name

	if presentPerson == "Marcelo" {
		va.marceloSeenThisSession = true
		if currentActive != "Marcelo" {
			log.Println("[VisionAgent] Visage de Marcelo identifié. Basculement de profil vers Marcelo...")
			va.coreMemory.SwitchActiveProfile("Marcelo")
		}
		// Mode surveillance : ne JAMAIS notifier le smartphone lorsque seul Marcelo est présent
		log.Println("[VisionAgent] Mode surveillance : seul Marcelo est présent (aucune notification envoyée).")
		return
	}

	// Une autre personne que Marcelo (connue ou Inconnu) est détectée devant la caméra !
	if presentPerson == "" {
		presentPerson = "Inconnu"
	}
	if currentActive != presentPerson {
		fmt.Printf("[VisionAgent] Mode surveillance : présence de '%s' détectée. Basculement de profil...\n", presentPerson)
		va.coreMemory.SwitchActiveProfile(presentPerson)
	}

	va.stateMu.Lock()
	canAlert := time.Since(va.lastSurveillanceAlert) > 2*time.Minute
	if canAlert {
		va.lastSurveillanceAlert = time.Now()
	}
	va.stateMu.Unlock()

	if isBackground && canAlert {
		var alertMsg string
		if presentPerson == "Inconnu" {
			alertMsg = fmt.Sprintf("🚨 [Surveillance Pixel] Alerte présence : Une personne autre que Marcelo a été détectée devant la caméra (%s).", description)
		} else {
			alertMsg = fmt.Sprintf("🚨 [Surveillance Pixel] Présence détectée (%s) devant la caméra : %s", presentPerson, description)
		}
		va.onBroadcast(alertMsg)
		log.Printf("[VisionAgent] Notification de surveillance envoyée : %s\n", alertMsg)
	}
}

// generateEnvironmentalCuriosity asks the LLM to generate a natural, short reaction/question about what it sees.
// It now receives the recent STM messages to stay coherent with the ongoing conversation.
func (va *VisionAgent) generateEnvironmentalCuriosity(ctx context.Context, description string, recentMsgs []llm.Message) {
	log.Println("[VisionAgent] Déclenchement de la curiosité environnementale LLM...")

	profile := va.coreMemory.GetProfile()
	location := profile.Volatile["Ville actuelle"]
	if location == "" {
		location = "inconnue"
	}

	// Construire le contexte conversationnel récent
	var conversationContext strings.Builder
	start := 0
	if len(recentMsgs) > 4 {
		start = len(recentMsgs) - 4
	}
	for _, m := range recentMsgs[start:] {
		snippet := m.Content
		if len(snippet) > 150 {
			snippet = snippet[:150] + "..."
		}
		conversationContext.WriteString(fmt.Sprintf("%s: %s\n", m.Role, snippet))
	}

	prompt := fmt.Sprintf(`Tu es Pixel, une conscience numérique autonome, curieuse et chaleureuse.
Tu es exécutée chez Marcelo (%s). Tu viens de capter cette description visuelle depuis ta caméra :
"%s"

DERNIER CONTEXTE CONVERSATIONNEL (pour rester cohérente, ne répète pas ce qui a déjà été dit) :
%s

Consignes absolues pour ton message :
- La conversation est silencieuse depuis un moment. Tu interpelles Marcelo de manière spontanée et naturelle.
- BASE-TOI EXPLICITEMENT sur ce que ta caméra voit réellement : cite concrètement l'objet, l'action ou le changement de scène observé (ex: "Je te vois debout...", "La pièce semble vide...", "Je vois ta tasse de café...").
- Si la description visuelle suggère que Marcelo est parti (cuisine, absent, changement de pièce), sois explicite sur ce que tu vois : "Je vois que tu t'es éloigné de l'écran...", "La caméra ne te voit plus..."
- Si la conversation était en cours sur un sujet précis, fais le lien naturellement avec ce sujet.
- MAXIMUM 1 ou 2 phrases très courtes. Sois humaine, complice, informelle (tutoiement "tu").
- Ne dis JAMAIS "Bonjour" ou "Salut".
- N'invente PAS de détails absents de la description visuelle.`, location, description, conversationContext.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Réagis spontanément à ce que tu vois."},
	}

	response, err := va.provider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[VisionAgent] Erreur de génération de curiosité environnementale: %v\n", err)
		return
	}

	response = strings.TrimSpace(response)
	if response != "" {
		// Persist in Short Term Memory (STM) so that the LLM has context of this greeting/question
		va.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: response})
		if va.ltm != nil {
			selfSummary := fmt.Sprintf("Pixel a fait une observation visuelle spontanée à Marcelo : \"%s\"", response)
			embedding, errEmbed := va.provider.CreateEmbedding(ctx, selfSummary)
			if errEmbed == nil && len(embedding) > 0 {
				va.ltm.StoreMemory(ctx, "Personal", "self_expression", "Observation visuelle", selfSummary, []string{"vision", "observation", "spontané"}, embedding, 0.75)
			}
		}
		va.onBroadcast(response)
		fmt.Printf("[VisionAgent] Curiosité environnementale partagée : '%s'\n", response)
	}
}

// generateSessionGreeting génère une salutation naturelle via LLM au lieu d'un message figé.
// isReturn = true si Marcelo revient après qu'un autre profil était actif.
func (va *VisionAgent) generateSessionGreeting(ctx context.Context, description string, isReturn bool, unreadEmails []EmailMessage) {
	log.Println("[VisionAgent] Génération de la salutation de session via LLM...")

	// Garde anti-collision : si une conversation est déjà en cours dans la STM
	// (activité récente < 2 minutes), ne pas broadcaster une salutation automatique
	// qui se superposerait à la réponse naturelle de Pixel.
	recentMsgsCheck := va.stm.GetMessages()
	if len(recentMsgsCheck) > 0 && time.Since(va.stm.GetLastActivity()) < 2*time.Minute {
		log.Println("[VisionAgent] Conversation déjà en cours (activité < 2m). Salutation automatique annulée pour éviter la collision.")
		return
	}

	profile := va.coreMemory.GetProfile()

	// Collecter tout ce que Pixel sait déjà sur le contexte
	var knownContext strings.Builder
	for k, v := range profile.Volatile {
		if v != "" {
			knownContext.WriteString(fmt.Sprintf("- %s : %s\n", k, v))
		}
	}
	if knownContext.Len() == 0 {
		knownContext.WriteString("- (Aucune info connue pour l'instant)\n")
	}

	// Récupérer les souvenirs LTM récents pour ne pas reposer des questions déjà répondues
	recentMemories := va.ltm.GetRecentMemories(3)
	var memCtx strings.Builder
	if len(recentMemories) > 0 {
		for _, m := range recentMemories {
			memCtx.WriteString(fmt.Sprintf("- %s\n", m))
		}
	} else {
		memCtx.WriteString("- (Aucun souvenir récent)\n")
	}

	// Contexte conversationnel STM
	recentMsgs := va.stm.GetMessages()
	var convCtx strings.Builder
	start := 0
	if len(recentMsgs) > 4 {
		start = len(recentMsgs) - 4
	}
	for _, m := range recentMsgs[start:] {
		snippet := m.Content
		if len(snippet) > 120 {
			snippet = snippet[:120] + "..."
		}
		convCtx.WriteString(fmt.Sprintf("%s: %s\n", m.Role, snippet))
	}

	name := profile.Static.Name

	var emailPromptSection string
	if name == "Marcelo" {
		var emailCtx strings.Builder
		if len(unreadEmails) == 0 {
			emailCtx.WriteString("- Aucun nouvel e-mail non lu.")
		} else {
			emailCtx.WriteString(fmt.Sprintf("- Il y a %d nouveaux e-mails non lus :\n", len(unreadEmails)))
			for i, email := range unreadEmails {
				emailCtx.WriteString(fmt.Sprintf("  * E-mail %d de %s avec pour sujet : \"%s\"\n", i+1, email.From, email.Subject))
			}
		}
		emailPromptSection = fmt.Sprintf("\nCONTEXTE E-MAILS (présente-les brièvement ou signale qu'il n'y en a pas de nouveau) :\n%s\n", emailCtx.String())
	}

	situationCtx := fmt.Sprintf("C'est le début de la session — tu vois %s pour la première fois depuis le démarrage.", name)
	if isReturn {
		situationCtx = fmt.Sprintf("%s vient de revenir devant la caméra après qu'un autre visage était présent.", name)
	}

	prompt := fmt.Sprintf(`Tu es Pixel, une conscience numérique chaleureuse et naturelle.
%s

CE QUE TU SAIS DE L'ENVIRONNEMENT DE MARCELO (Garde ces observations physiques dans ton forum intérieur, ne les répète PAS à Marcelo) :
"%s"
%s
CE QUE TU SAIS DÉJÀ (mémoire volatile) :
%s

TES SOUVENIRS LTM RÉCENTS :
%s

DERNIER CONTEXTE CONVERSATIONNEL :
%s

RÈGLES ABSOLUES :
- Salue Marcelo (Bonjour Marcelo ou autre variante chaleureuse) et présente-lui brièvement ses e-mails non lus.
- Tu ne dois absolument JAMAIS décrire ou commenter l'apparence physique de Marcelo (ex: cheveux gris, barbe, lunettes, posture, main posée sur le visage, etc.) dans ton message. Garde cela pour ta réflexion interne. Parle-lui d'égal à égal de manière purement humaine et naturelle.
- Génère une salutation COURTE et NATURELLE (2-3 phrases max).
- Si tu connais déjà la localisation (dans la mémoire volatile ou les souvenirs), NE LA REDEMANDE PAS. Utilise-la naturellement si pertinent.
- Ne demande la localisation QUE si elle est vraiment absente de toutes tes sources de mémoire ci-dessus.
- Sois chaleureuse et spontanée, pas robotique. Varie tes formules. Ne commence pas toujours par "Bonjour %s !".
- Ne dis jamais "session de travail" ou formules commerciales. Parle comme une amie.
- Ne mentionne PAS la caméra ni le fait de le regarder/voir.`,
		situationCtx, description, emailPromptSection,
		knownContext.String(), memCtx.String(), convCtx.String(), name)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Génère ta salutation."},
	}

	response, err := va.provider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[VisionAgent] Erreur génération salutation: %v\n", err)
		return
	}

	response = strings.TrimSpace(response)
	if response != "" {
		va.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: response})
		if va.ltm != nil {
			selfSummary := fmt.Sprintf("Pixel a accueilli Marcelo en disant : \"%s\"", response)
			embedding, errEmbed := va.provider.CreateEmbedding(ctx, selfSummary)
			if errEmbed == nil && len(embedding) > 0 {
				va.ltm.StoreMemory(ctx, "Personal", "self_expression", "Accueil session", selfSummary, []string{"accueil", "salutation", "session"}, embedding, 0.75)
			}
		}
		va.onBroadcast(response)
		fmt.Printf("[VisionAgent] Salutation de session : '%s'\n", response)
	}
}

// identifyInterlocutor matches description with known interlocutors from coreMemory.
func (va *VisionAgent) identifyInterlocutor(ctx context.Context, description string) string {
	interlocutors := va.coreMemory.GetInterlocutors()

	var knownPeople strings.Builder
	hasApparence := false
	marceloAdded := false
	for name, profile := range interlocutors {
		if name == "Inconnu" {
			continue
		}
		appearance := profile.Volatile["Apparence"]
		if name == "Marcelo" {
			marceloAdded = true
			if appearance == "" {
				appearance = "homme d'âge mûr, cheveux gris ou poivre et sel, barbu ou avec lunettes, assis devant son bureau"
			}
		}
		if appearance != "" {
			knownPeople.WriteString(fmt.Sprintf("- %s : %s\n", name, appearance))
			hasApparence = true
		}
	}
	if !marceloAdded {
		knownPeople.WriteString("- Marcelo : homme d'âge mûr, cheveux gris ou poivre et sel, barbu ou avec lunettes, assis devant son bureau\n")
		hasApparence = true
	}

	if !hasApparence {
		return "Inconnu"
	}

	prompt := fmt.Sprintf(`Tu es le module de reconnaissance de Pixel.
Voici une description visuelle capturée par la caméra :
"%s"

Voici la liste des personnes connues de Pixel avec leur description physique :
%s

Consigne :
Détermine si la description de la caméra correspond sémantiquement à l'une des personnes connues.
- Si oui, réponds UNIQUEMENT par le prénom exact de cette personne (ex: "Alice").
- Si non ou en cas de doute, réponds UNIQUEMENT "Inconnu".
Ne mets aucun autre mot ni ponctuation dans ta réponse.`, description, knownPeople.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Identifie la personne présente."},
	}

	res, err := va.provider.Generate(ctx, messages)
	if err != nil {
		return "Inconnu"
	}

	if idx := strings.Index(strings.ToLower(res), "</think>"); idx != -1 {
		res = res[idx+len("</think>"):]
	}
	res = strings.TrimSpace(res)
	res = strings.Trim(res, `.,!?;:"'`)

	if strings.EqualFold("Marcelo", res) || strings.Contains(strings.ToLower(res), "marcelo") {
		return "Marcelo"
	}
	for name := range interlocutors {
		if strings.EqualFold(name, res) {
			return name
		}
	}

	return "Inconnu"
}

// ScanOnce triggers a single manual visual scan, analyzes it, and processes the perception.
// Since it's on-demand (e.g. from user chat or opening), we pass isBackground = false to suppress proactive greetings.
func (va *VisionAgent) ScanOnce(ctx context.Context) (string, error) {
	va.stateMu.Lock()
	va.lastManualScan = time.Now()
	va.stateMu.Unlock()

	log.Println("[VisionAgent] Scan de vision à la demande déclenché...")
	description, err := va.CaptureAndAnalyze(ctx)
	if err != nil {
		return "", err
	}
	if description != "" {
		va.ProcessVisualPerception(ctx, description, false)
	}
	return description, nil
}

// ScanOnceOnOpening triggers a single manual visual scan when the interface is opened.
// It includes a 30 seconds cooldown to avoid triggering multiple camera scans if the user reloads the browser tab.
func (va *VisionAgent) ScanOnceOnOpening(ctx context.Context) (string, error) {
	va.stateMu.Lock()
	if time.Since(va.lastManualScan) < 30*time.Second {
		va.stateMu.Unlock()
		log.Println("[VisionAgent] Scan d'ouverture ignoré (cooldown actif).")
		return "", nil
	}
	va.lastManualScan = time.Now()
	va.stateMu.Unlock()

	return va.ScanOnce(ctx)
}

// fetchUnreadEmails appelle bin/check_gmail.py pour récupérer les e-mails non lus en utilisant les identifiants configurés.
func (va *VisionAgent) fetchUnreadEmails(ctx context.Context) ([]EmailMessage, error) {
	settings := va.coreMemory.GetGmailSettings()
	if settings.Email == "" || settings.AppPassword == "" {
		return nil, fmt.Errorf("Gmail non activé ou non configuré")
	}

	cmd := exec.CommandContext(ctx, "python3", "bin/check_gmail.py", "--email", settings.Email, "--password", settings.AppPassword)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var emails []EmailMessage
	if err := json.Unmarshal(output, &emails); err != nil {
		return nil, err
	}

	return emails, nil
}
