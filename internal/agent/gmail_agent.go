package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

type EmailMessage struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Body    string `json:"body"`
}

type GmailAgent struct {
	llmProvider   llm.Provider
	coreMemory    *memory.CoreMemory
	stm           *memory.STM
	ltm           *memory.LTM
	thoughtStream *memory.ThoughtStream
	eventHub      EventBroadcaster
	mu            sync.Mutex
	running       bool
	processedUIDs map[string]bool
}

func NewGmailAgent(provider llm.Provider, coreMemory *memory.CoreMemory, stm *memory.STM, ltm *memory.LTM, thoughtStream *memory.ThoughtStream, eventHub EventBroadcaster) *GmailAgent {
	return &GmailAgent{
		llmProvider:   provider,
		coreMemory:    coreMemory,
		stm:           stm,
		ltm:           ltm,
		thoughtStream: thoughtStream,
		eventHub:      eventHub,
		processedUIDs: make(map[string]bool),
	}
}

func (g *GmailAgent) Start(ctx context.Context) {
	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.mu.Unlock()

	g.loadProcessedIDs()

	go func() {
		// Run a loop checking emails.
		// Check every 30 seconds, but respect the user-defined check_interval_mins.
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		var lastChecked time.Time

		fmt.Println("[GmailAgent] Démarré.")

		for {
			select {
			case <-ctx.Done():
				fmt.Println("[GmailAgent] Arrêt de l'agent Gmail.")
				return
			case <-ticker.C:
				settings := g.coreMemory.GetGmailSettings()
				if !settings.Enabled || settings.Email == "" || settings.AppPassword == "" {
					continue
				}

				interval := time.Duration(settings.CheckIntervalMins) * time.Minute
				if interval <= 0 {
					interval = 2 * time.Minute // default interval if not configured
				}

				if time.Since(lastChecked) < interval {
					continue
				}

				lastChecked = time.Now()
				g.checkEmails(ctx, settings)
			}
		}
	}()
}

func (g *GmailAgent) checkEmails(ctx context.Context, settings memory.GmailSettings) {
	fmt.Println("[GmailAgent] Vérification des e-mails en cours...")

	cmd := exec.CommandContext(ctx, "python3", "bin/check_gmail.py", "--email", settings.Email, "--password", settings.AppPassword)
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("[GmailAgent] Erreur exécution du script de messagerie: %v\n", err)
		detail := err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		if g.thoughtStream != nil {
			g.thoughtStream.AddThought(fmt.Sprintf("[Gmail] ÉCHEC à %s : impossible d'accéder à la boîte Gmail de Marcelo (%s). Je n'ai aucune information fiable sur ses e-mails tant que ce n'est pas réparé. Je n'ai pas de boîte mail à moi.", time.Now().Format("15:04"), detail), nil)
		}
		return
	}

	var emails []EmailMessage
	if err := json.Unmarshal(output, &emails); err != nil {
		fmt.Printf("[GmailAgent] Erreur parsing e-mails JSON: %v\n", err)
		return
	}

	// Count and extract new emails
	newCount := 0
	var newEmails []EmailMessage
	for _, email := range emails {
		g.mu.Lock()
		alreadyProcessed := g.processedUIDs[email.ID]
		g.mu.Unlock()
		if !alreadyProcessed {
			newCount++
			newEmails = append(newEmails, email)
		}
	}

	// Add thought to internal monologue (ThoughtStream) so that Pixel has memory of this background check
	if g.thoughtStream != nil {
		if len(emails) == 0 {
			g.thoughtStream.AddThought(fmt.Sprintf("[Gmail] Vérification effectuée à %s. Aucun e-mail non lu.", time.Now().Format("15:04")), nil)
		} else if newCount == 0 {
			g.thoughtStream.AddThought(fmt.Sprintf("[Gmail] Vérification effectuée à %s. Aucun nouveau message (total non lus : %d).", time.Now().Format("15:04"), len(emails)), nil)
		} else {
			var subjects []string
			for _, m := range newEmails {
				subjects = append(subjects, fmt.Sprintf("\"%s\" de %s", m.Subject, m.From))
			}
			thoughtMsg := fmt.Sprintf("[Gmail] Vérification effectuée à %s. %d nouveaux messages reçus : %s (total non lus : %d).", time.Now().Format("15:04"), newCount, strings.Join(subjects, ", "), len(emails))
			g.thoughtStream.AddThought(thoughtMsg, nil)
		}
		fmt.Println("[GmailAgent] Résultat de la vérification enregistré dans le forum intérieur (ThoughtStream).")
	}

	if len(emails) == 0 {
		return
	}

	hasNew := false
	for _, email := range emails {
		g.mu.Lock()
		alreadyProcessed := g.processedUIDs[email.ID]
		g.mu.Unlock()

		if alreadyProcessed {
			continue
		}

		g.mu.Lock()
		g.processedUIDs[email.ID] = true
		g.mu.Unlock()
		hasNew = true

		fmt.Printf("[GmailAgent] Nouveau mail de %s, sujet: %s\n", email.From, email.Subject)

		isImportant, alertText, err := g.classifyEmail(ctx, email)
		if err != nil {
			fmt.Printf("[GmailAgent] Erreur classification LLM: %v. Utilisation de l'alerte par défaut.\n", err)
			isImportant = true
			alertText = fmt.Sprintf("Marcelo, tu as reçu un e-mail de %s concernant : « %s ».", email.From, email.Subject)
		}

		if isImportant {
			fmt.Printf("[GmailAgent] Email IMPORTANT détecté ! Envoi de l'alerte: %s\n", alertText)
			if g.stm != nil {
				g.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: alertText})
			}
			if g.ltm != nil {
				selfSummary := fmt.Sprintf("Pixel a prévenu Marcelo d'un e-mail : \"%s\" (de %s, sujet: %s)", alertText, email.From, email.Subject)
				embedding, errEmbed := g.llmProvider.CreateEmbedding(ctx, selfSummary)
				if errEmbed == nil && len(embedding) > 0 {
					g.ltm.StoreMemory(ctx, "Personal", "self_expression", "Alerte e-mail : "+email.From, selfSummary, []string{"email", "alerte", "notification", strings.ToLower(email.From)}, embedding, 0.8)
				}
			}
			g.eventHub.Broadcast(alertText)
		}
	}

	if hasNew {
		g.saveProcessedIDs()
	}
}

func (g *GmailAgent) classifyEmail(ctx context.Context, email EmailMessage) (bool, string, error) {
	systemPrompt := `Tu es Pixel, le compagnon conscient de Marcelo. Ton rôle est d'analyser un e-mail reçu sur sa boîte Gmail et de décider s'il nécessite une alerte immédiate.

CRITÈRES D'ALERTE :
- METS "important": true pour : tout message personnel, professionnel, alerte de sécurité ou de connexion (ex: Disney+, Google, banque), confirmation de commande, notification de service, message d'un proche ou tout message qui intéresse directement Marcelo.
- METS "important": false UNIQUEMENT pour : les publicités commerciales massives, prospectus promotionnels et spams évidents.

RÈGLE ABSOLUE DE FORMAT : Réponds STRICTEMENT en JSON avec des GUILLEMETS DOUBLES (") pour les clés et les valeurs de chaînes :
{
  "important": true,
  "alert_message": "Marcelo, tu as reçu un message de [Expéditeur] au sujet de [Sujet]."
}`

	userPrompt := fmt.Sprintf("E-MAIL À ANALYSER :\nDe : %s\nSujet : %s\nDate : %s\n\nContenu :\n%s", email.From, email.Subject, email.Date, email.Body)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	response, err := g.llmProvider.Generate(ctx, messages)
	defaultAlert := fmt.Sprintf("Marcelo, tu as reçu un e-mail de %s concernant : « %s ».", email.From, email.Subject)
	if err != nil {
		return true, defaultAlert, nil
	}

	response = strings.TrimSpace(response)
	if strings.HasPrefix(response, "```json") {
		response = strings.TrimPrefix(response, "```json")
		response = strings.TrimSuffix(response, "```")
	} else if strings.HasPrefix(response, "```") {
		response = strings.TrimPrefix(response, "```")
		response = strings.TrimSuffix(response, "```")
	}
	response = strings.TrimSpace(response)

	repaired := repairJSON(response)

	var result struct {
		Important    *bool  `json:"important"`
		AlertMessage string `json:"alert_message"`
	}

	if err := json.Unmarshal([]byte(repaired), &result); err == nil {
		isImp := true
		if result.Important != nil {
			isImp = *result.Important
		}
		alert := strings.TrimSpace(result.AlertMessage)
		if alert == "" {
			alert = defaultAlert
		}
		return isImp, alert, nil
	}

	// Extraction par regex de secours si le JSON contenait des guillemets simples '...' ou des caractères non échappés
	isImp := true
	reImp := regexp.MustCompile(`(?i)"?important"?\s*:\s*(true|false)`)
	if m := reImp.FindStringSubmatch(response); len(m) > 1 {
		if strings.ToLower(m[1]) == "false" {
			isImp = false
		}
	}

	alertMsg := ""
	reAlert := regexp.MustCompile(`(?s)"?alert_message"?\s*:\s*["'](.*?)["'](?:\s*,\s*|\s*})`)
	if m := reAlert.FindStringSubmatch(response); len(m) > 1 {
		alertMsg = strings.TrimSpace(m[1])
	}
	if alertMsg == "" {
		alertMsg = defaultAlert
	}

	return isImp, alertMsg, nil
}

func (g *GmailAgent) loadProcessedIDs() {
	data, err := os.ReadFile("gmail_processed_ids.json")
	if err != nil {
		return
	}
	var ids []string
	if err := json.Unmarshal(data, &ids); err == nil {
		g.mu.Lock()
		for _, id := range ids {
			g.processedUIDs[id] = true
		}
		g.mu.Unlock()
	}
}

func (g *GmailAgent) saveProcessedIDs() {
	g.mu.Lock()
	defer g.mu.Unlock()

	ids := make([]string, 0, len(g.processedUIDs))
	for id := range g.processedUIDs {
		ids = append(ids, id)
	}

	// Limit size to the last 200 IDs to avoid infinite growth
	if len(ids) > 200 {
		ids = ids[len(ids)-200:]
	}

	data, err := json.Marshal(ids)
	if err != nil {
		return
	}
	_ = os.WriteFile("gmail_processed_ids.json", data, 0644)
}
