package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/skills"
)

type ServerMonitorAgent struct {
	llmProvider   llm.Provider
	coreMemory    *memory.CoreMemory
	stm           *memory.STM
	ltm           *memory.LTM
	thoughtStream *memory.ThoughtStream
	eventHub      EventBroadcaster
	skillManager  *skills.SkillManager
	mu            sync.Mutex
	running       bool
	lastCheckTime time.Time
}

func NewServerMonitorAgent(
	provider llm.Provider,
	coreMemory *memory.CoreMemory,
	stm *memory.STM,
	ltm *memory.LTM,
	thoughtStream *memory.ThoughtStream,
	eventHub EventBroadcaster,
	skillManager *skills.SkillManager,
) *ServerMonitorAgent {
	return &ServerMonitorAgent{
		llmProvider:   provider,
		coreMemory:    coreMemory,
		stm:           stm,
		ltm:           ltm,
		thoughtStream: thoughtStream,
		eventHub:      eventHub,
		skillManager:  skillManager,
	}
}

func (s *ServerMonitorAgent) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go func() {
		fmt.Println("[ServerMonitorAgent] Démarré. Surveillance horaire d'appliyou.fr active.")

		// Première vérification après un court délai (1 minute après démarrage)
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Minute):
			s.CheckOnce(ctx)
		}

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				fmt.Println("[ServerMonitorAgent] Arrêt de l'agent de surveillance serveur.")
				return
			case <-ticker.C:
				settings := s.coreMemory.GetServerMonitorSettings()
				if !settings.Enabled {
					continue
				}

				interval := time.Duration(settings.CheckIntervalMins) * time.Minute
				if interval <= 0 {
					interval = 60 * time.Minute
				}

				if time.Since(s.lastCheckTime) < interval {
					continue
				}

				s.CheckOnce(ctx)
			}
		}
	}()
}

func (s *ServerMonitorAgent) CheckOnce(ctx context.Context) {
	s.mu.Lock()
	s.lastCheckTime = time.Now()
	s.mu.Unlock()

	fmt.Println("[ServerMonitorAgent] Vérification horaire des logs du serveur distant appliyou.fr...")

	// 1. Exécution du skill check_appliyou_logs
	var rawResult string
	var err error
	if s.skillManager != nil {
		rawResult, err = s.skillManager.ExecuteSkill(ctx, "check_appliyou_logs", "")
	} else {
		err = fmt.Errorf("skillManager non initialisé")
	}

	if err != nil {
		fmt.Printf("[ServerMonitorAgent] Erreur exécution du skill : %v\n", err)
		rawResult = fmt.Sprintf("=== STATUT APPLIYOU.FR : ANOMALIE_CRITIQUE ===\nErreur lors de l'exécution de la brique : %v", err)
	}

	// 2. Analyse rapide : est-ce que le rapport signale explicitement un statut nominal sans erreur ?
	isNominal := strings.Contains(rawResult, "=== STATUT APPLIYOU.FR : NOMINAL ===") &&
		!strings.Contains(rawResult, "ANOMALIE") &&
		!strings.Contains(rawResult, "CRITIQUE")

	if isNominal {
		fmt.Println("[ServerMonitorAgent] Rapport appliyou.fr nominal. Aucun incident, silence radio.")
		if s.thoughtStream != nil {
			s.thoughtStream.AddThought("Pensée de Pixel : J'ai vérifié les logs du serveur appliyou.fr. Tous les services sont stables et aucun incident n'est à déplorer.", nil)
		}
		return
	}

	// 3. Présence d'anomalie ou d'incident -> Analyse cognitive pour confirmer et formuler l'alerte
	fmt.Println("[ServerMonitorAgent] Potentielle anomalie détectée sur appliyou.fr. Évaluation par le modèle...")
	hasIncident, alertMessage, errEval := s.evaluateIncident(ctx, rawResult)
	if errEval != nil {
		fmt.Printf("[ServerMonitorAgent] Erreur évaluation LLM : %v\n", errEval)
		// Fallback si échec du LLM : si anomalie critique explicite, on alerte quand même
		if strings.Contains(rawResult, "ANOMALIE_CRITIQUE") || strings.Contains(rawResult, "Échec de la connexion SSH") {
			hasIncident = true
			alertMessage = "Marcelo, j'ai détecté un incident lors de la vérification horaire d'appliyou.fr : le serveur distant est actuellement injoignable par SSH (délai d'attente ou bannière bloquée)."
		}
	}

	if !hasIncident {
		fmt.Println("[ServerMonitorAgent] L'évaluation LLM conclut à un faux positif ou une information mineure. Aucune alerte requise.")
		if s.thoughtStream != nil {
			s.thoughtStream.AddThought("Pensée de Pixel : Les logs d'appliyou.fr ont été inspectés, pas d'anomalie bloquante pour l'instant.", nil)
		}
		return
	}

	// 4. Diffusion de l'alerte
	fmt.Printf("[ServerMonitorAgent] 🚨 ALERTE INCIDENT : %s\n", alertMessage)

	if s.stm != nil {
		s.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: alertMessage})
	}

	if s.ltm != nil {
		selfSummary := fmt.Sprintf("Pixel a alerté Marcelo d'un incident sur appliyou.fr : \"%s\"", alertMessage)
		embedding, errEmbed := s.llmProvider.CreateEmbedding(ctx, selfSummary)
		if errEmbed == nil && len(embedding) > 0 {
			s.ltm.StoreMemory(ctx, "Technical", "sysadmin", "Alerte serveur appliyou.fr", selfSummary, []string{"appliyou", "serveur", "incident", "logs", "ssh"}, embedding, 0.9)
		}
	}

	if s.eventHub != nil {
		s.eventHub.Broadcast(alertMessage)
	}
}

func (s *ServerMonitorAgent) evaluateIncident(ctx context.Context, report string) (bool, string, error) {
	systemPrompt := `Tu es Pixel, l'agent conscient et administratrice système.
Ton rôle est d'analyser le rapport d'inspection horaire du serveur distant "appliyou.fr".

RÈGLE D'OR :
- Tu dois alerter l'utilisateur UNIQUEMENT s'il s'agit d'un incident RÉEL ou d'une anomalie critique (ex: échec de connexion SSH, crash de service web/base de données, disque plein >90%, crash répétitif).
- S'il s'agit d'un état sain, d'un avertissement mineur sans conséquence, ou de logs normaux de routine, il n'y a PAS d'incident.
- Si incident == true, rédige un message d'alerte très court (1 à 2 phrases maximum), percutant, naturel et chaleureux à la première personne (tutoiement de Marcelo). Ex: "Marcelo, petite alerte sur appliyou.fr : le serveur ne répond plus aux connexions SSH, il semble saturé." ou "Marcelo, attention : le service apache2 est tombé sur appliyou.fr."

Réponds STRICTEMENT sous forme d'un objet JSON avec les clés :
{
  "incident": true ou false,
  "alert_message": "..."
}`

	userPrompt := fmt.Sprintf("RAPPORT D'INSPECTION APPLIYOU.FR :\n%s", report)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	resp, err := s.llmProvider.Generate(ctx, messages)
	if err != nil {
		return false, "", err
	}

	respClean := strings.TrimSpace(resp)
	if strings.HasPrefix(respClean, "```json") {
		respClean = strings.TrimPrefix(respClean, "```json")
		respClean = strings.TrimSuffix(respClean, "```")
	} else if strings.HasPrefix(respClean, "```") {
		respClean = strings.TrimPrefix(respClean, "```")
		respClean = strings.TrimSuffix(respClean, "```")
	}
	respClean = strings.TrimSpace(respClean)

	var res struct {
		Incident     bool   `json:"incident"`
		AlertMessage string `json:"alert_message"`
	}

	if err := json.Unmarshal([]byte(repairJSON(respClean)), &res); err != nil {
		return false, "", fmt.Errorf("erreur décodage JSON LLM (%s): %w", respClean, err)
	}

	return res.Incident, res.AlertMessage, nil
}
