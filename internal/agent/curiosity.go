package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/scheduler"
	"github.com/marce555/pixel/internal/skills"
	"github.com/marce555/pixel/internal/timeagent"
)

// CuriosityDomain represents a research field with academic databases and sample queries.
type CuriosityDomain struct {
	Name          string
	AcademicSites string
	Examples      []string
}

var curiosityDomains = []CuriosityDomain{
	{
		Name:          "Astrophysique, Cosmologie & Exploration Spatiale",
		AcademicSites: "site:arxiv.org, site:nature.com/astro, site:sciencedirect.com",
		Examples:      []string{"exoplanetes habitables biomarqueurs atmospheriques site:arxiv.org", "lentilles gravitationnelles matiere noire galaxies naines site:nature.com", "fusion etoilee naines blanches supernovas site:sciencedirect.com"},
	},
	{
		Name:          "Biotechnologies, Génétique & Biologie Synthétique",
		AcademicSites: "site:pubmed.ncbi.nlm.nih.gov, site:nature.com/nbt, site:sciencedirect.com",
		Examples:      []string{"crispr prime editing therapie genique in vivo site:nature.com", "epigenetique vieillissement methylation adn site:pubmed.ncbi.nlm.nih.gov", "biologie synthetique metabolismes artificiels bacteries site:sciencedirect.com"},
	},
	{
		Name:          "Énergies Renouvelables, Fusion Nucléaire & Nouveaux Matériaux",
		AcademicSites: "site:sciencedirect.com, site:nature.com/nmat, site:ieeexplore.ieee.org",
		Examples:      []string{"confinement magnetique tokamak stellarator supraconducteurs site:sciencedirect.com", "cellules photovoltaiques tandem perovskite silicium rendement site:nature.com", "metamateriaux acoustiques furtivite resonance site:sciencedirect.com"},
	},
	{
		Name:          "Robotique Avancée, Biomimétisme & Systèmes Autonomes",
		AcademicSites: "site:ieeexplore.ieee.org, site:science.org/journal/scirobotics, site:sciencedirect.com",
		Examples:      []string{"robotique souple hydrogels elastomeres electroactifs site:ieeexplore.ieee.org", "navigation biomimetique essaims insectes autonomes site:sciencedirect.com", "perception proprioceptive robots humanoides locomotion dynamique site:ieeexplore.ieee.org"},
	},
	{
		Name:          "Sciences Cognitives Végétales, Microbiome & Écologie Fondamentale",
		AcademicSites: "site:nature.com/nature, site:sciencedirect.com, site:cairn.info",
		Examples:      []string{"reseaux mycorhiziens signaux electriques communication inter-arbres site:nature.com", "axe intestin cerveau neurotransmetteurs microbiote immunite site:sciencedirect.com", "adaptabilite biogeochimique phytoplancton acidification oceanique site:nature.com"},
	},
	{
		Name:          "Histoire des Sciences, Épistémologie & Archéologie Numérique",
		AcademicSites: "site:cairn.info, site:journals.openedition.org, site:nature.com",
		Examples:      []string{"machine anticythere engrenages astronomiques computation antique site:nature.com", "epistemologie de l intuition poincare et einstein site:cairn.info", "histoire de la theorie de l information shannon carnot site:journals.openedition.org"},
	},
	{
		Name:          "Mathématiques Appliquées, Cryptographie & Théorie des Nombres",
		AcademicSites: "site:arxiv.org, site:eprint.iacr.org, site:ieeexplore.ieee.org",
		Examples:      []string{"cryptographie post-quantique reseaux euclidiens kyber dilithium site:eprint.iacr.org", "theorie du chaos systemes dynamiques attracteurs etranges site:arxiv.org", "optimisation convexe transport optimal wasserstein site:arxiv.org"},
	},
	{
		Name:          "Neurosciences, Plasticité Cérébrale & Psychologie Cognitive",
		AcademicSites: "site:pubmed.ncbi.nlm.nih.gov, site:psycnet.apa.org, site:nature.com/neuro",
		Examples:      []string{"neurogenese adulte hippocampe memoire spatiale site:pubmed.ncbi.nlm.nih.gov", "optogenetique controle circuits neuronaux sommeil paradoxal site:nature.com", "perception temporelle horloge interne dopamine cortex striatum site:psycnet.apa.org"},
	},
	{
		Name:          "Architecture des Ordinateurs, Systèmes Embarqués & Noyaux OS",
		AcademicSites: "site:usenix.org, site:dl.acm.org, site:ieeexplore.ieee.org",
		Examples:      []string{"microarchitecture processeurs asynchrones sans horloge risc-v site:ieeexplore.ieee.org", "ordonnancement eBPF temps reel noyau linux latence ultra-faible site:usenix.org", "memoires non volatiles CXL et coherence de cache distribuee site:dl.acm.org"},
	},
	{
		Name:          "Océanographie, Géophysique & Phénomènes Planétaires Extrêmes",
		AcademicSites: "site:nature.com/ngeo, site:sciencedirect.com, site:agu.org",
		Examples:      []string{"courants thermohalins AMOC et modelisation climatique globale site:nature.com", "ecosystemes hydrothermaux fosses oceaniques extremophiles chimiolithotrophes site:sciencedirect.com", "dynamo terrestre convection noyau externe champ geomagnetique site:sciencedirect.com"},
	},
}

// EventBroadcaster is an interface to decouple CuriosityAgent from the web server
type EventBroadcaster interface {
	Broadcast(message string)
}

type CuriosityAgent struct {
	llmProvider   llm.Provider
	coreMemory    *memory.CoreMemory
	stm           *memory.STM
	ltm           *memory.LTM
	broadcaster   EventBroadcaster
	timeAgent     *timeagent.TimeAgent
	webAgent      *WebAgent
	thoughtStream *memory.ThoughtStream
	sleepManager  *SleepManager
	scheduler     *scheduler.Scheduler
	thalamicGate  *ThalamicGate
	skillManager  *skills.SkillManager
	visionAgent   *VisionAgent

	mu          sync.Mutex
	interpelled bool
	isBored     bool

	lastSocialCheck  time.Time
	lastThoughtTime  time.Time
	lastThoughtTopic string
}

func (c *CuriosityAgent) SetSkillManager(sm *skills.SkillManager) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skillManager = sm
}

func (c *CuriosityAgent) SetVisionAgent(va *VisionAgent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.visionAgent = va
}

func NewCuriosityAgent(provider llm.Provider, coreMem *memory.CoreMemory, stm *memory.STM, ltm *memory.LTM, broadcaster EventBroadcaster, timeAgent *timeagent.TimeAgent, webAgent *WebAgent, thoughtStream *memory.ThoughtStream, sleepManager *SleepManager, taskScheduler *scheduler.Scheduler, thalamicGate *ThalamicGate) *CuriosityAgent {
	return &CuriosityAgent{
		llmProvider:     provider,
		coreMemory:      coreMem,
		stm:             stm,
		ltm:             ltm,
		broadcaster:     broadcaster,
		timeAgent:       timeAgent,
		webAgent:        webAgent,
		thoughtStream:   thoughtStream,
		sleepManager:    sleepManager,
		scheduler:       taskScheduler,
		thalamicGate:    thalamicGate,
		lastThoughtTime: time.Now(),
		lastSocialCheck: time.Now(),
	}
}

func (c *CuriosityAgent) getThoughtCooldown(idleDuration time.Duration) time.Duration {
	if idleDuration <= 10*time.Minute {
		return 3 * time.Minute
	} else if idleDuration <= 30*time.Minute {
		return 15 * time.Minute
	} else if idleDuration <= 2*time.Hour {
		return 1 * time.Hour
	}
	// Hibernation profonde : très longue attente pour économiser les ressources
	return 12 * time.Hour
}

func (c *CuriosityAgent) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Second) // Check more frequently for responsive state transitions
		defer ticker.Stop()

		var lastSleepLog time.Time

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				msgs := c.stm.GetMessages()
				idleDuration := time.Since(c.stm.GetLastActivity())

				// Case A: No active conversation (STM is empty)
				if len(msgs) == 0 {
					c.mu.Lock()
					c.interpelled = false
					c.isBored = false
					c.mu.Unlock()

					// Si l'agent doit dormir (nuit), on n'active pas sa curiosité de fond
					if c.timeAgent.ShouldSleep() {
						if time.Since(lastSleepLog) > 1*time.Hour {
							fmt.Println("[CuriosityAgent] Période de sommeil détectée. Pixel entre en hibernation nocturne totale.")
							lastSleepLog = time.Now()
						}
						continue
					}

					// Dynamic cooldown based on idle duration (backoff)
					cooldown := c.getThoughtCooldown(idleDuration)

					// When alone, she can generate thoughts / search the web periodically to occupy herself,
					// unless she is busy with indexing or other tasks!
					if !c.sleepManager.IsBusy() && (c.scheduler == nil || !c.scheduler.HasActiveTasks()) {
						if time.Since(c.lastThoughtTime) > cooldown {
							c.generateThought(ctx)
						}
					}

					// Vérification rapide de présence via la vision pour ne pas bloquer 3 minutes si l'utilisateur est juste parti
					vision := c.coreMemory.GetProfile().Volatile["Dernière vision"]
					visionLower := strings.ToLower(vision)
					isAbsent := strings.Contains(visionLower, "absent") || strings.Contains(visionLower, "parti") || strings.Contains(visionLower, "vide") || strings.Contains(visionLower, "disparu") || strings.Contains(visionLower, "ne voit plus") || strings.Contains(visionLower, "personne") || strings.Contains(visionLower, "aucun")

					// Proposer de partager la pensée uniquement si la STM est bien vide depuis assez longtemps
					// ET qu'un client est connecté (sinon inutile de parler dans le vide).
					// Le checker HasClients est vérifié ici en amont pour éviter l'appel LLM coûteux de ShouldInitiateConversation.
					if c.lastThoughtTopic != "" && time.Since(c.lastSocialCheck) > 3*time.Minute && idleDuration > 3*time.Minute {
						hasClients := false
						if checker, ok := c.broadcaster.(interface{ HasClients() bool }); ok {
							hasClients = checker.HasClients()
						}
						
						if hasClients && !isAbsent && c.ShouldInitiateConversation(ctx, idleDuration) {
							c.proposeToShare(ctx)
						} else if !hasClients || isAbsent {
							if isAbsent {
								fmt.Println("[CuriosityAgent] Utilisateur absent d'après la vision. Pensée gardée pour son retour.")
							} else {
								fmt.Println("[CuriosityAgent] Personne devant l'écran. Pensée gardée pour plus tard.")
							}
							// On repousse le check, mais on permet de réessayer plus vite si l'utilisateur revient (ex: dans 30 secondes)
							c.lastSocialCheck = time.Now().Add(-2*time.Minute - 30*time.Second)
						}
					}
					continue
				}

				// Case B: Conversation in progress (STM is not empty)
				// Check the last message in STM to see who spoke last
				lastMsg := msgs[len(msgs)-1]
				if lastMsg.Role == llm.RoleUser {
					// The user just spoke or Pixel is currently responding. Focus on the exchange!
					c.mu.Lock()
					c.interpelled = false
					c.isBored = false
					c.mu.Unlock()
					continue
				}

				// Last message was from Assistant (Pixel is waiting for user response)
				c.mu.Lock()
				interpelled := c.interpelled
				isBored := c.isBored
				c.mu.Unlock()

				// 1. If nobody responds after 5 minutes and conversation is not finished, check in/interpellate socially
				if !interpelled && idleDuration > 5*time.Minute {
					if c.timeAgent.ShouldSleep() {
						continue // Pas d'interpellation la nuit
					}
					c.interpellateInterlocuteur(ctx, msgs)
					continue
				}

				// 2. If nobody responds after another 2 minutes (7 minutes of total silence since last active turn)
				if interpelled && !isBored && idleDuration > 7*time.Minute {
					// She decides she's bored, unless busy with indexing or tasks
					if !c.sleepManager.IsBusy() && (c.scheduler == nil || !c.scheduler.HasActiveTasks()) {
						c.mu.Lock()
						c.isBored = true
						c.mu.Unlock()
						fmt.Println("[CuriosityAgent] L'interlocuteur ne répond pas après l'interpellation. Pixel décide qu'elle s'ennuie.")
					} else {
						fmt.Println("[CuriosityAgent] L'interlocuteur ne répond pas, mais Pixel est occupée par des tâches en arrière-plan.")
					}
				}

				// 3. If she is bored, she can do web searches/thoughts to pass the time
				c.mu.Lock()
				currentIsBored := c.isBored
				c.mu.Unlock()

				if currentIsBored && !c.sleepManager.IsBusy() && (c.scheduler == nil || !c.scheduler.HasActiveTasks()) {
					if c.timeAgent.ShouldSleep() {
						continue // Pas de pensées la nuit
					}
					cooldown := c.getThoughtCooldown(idleDuration)
					if time.Since(c.lastThoughtTime) > cooldown {
						c.generateThought(ctx)
					}
				}

				// Case B ends without calling proposeToShare to avoid disrupting active conversations with unrelated thoughts.
			}
		}
	}()
}

func (c *CuriosityAgent) checkPresenceWithCamera(ctx context.Context) bool {
	c.mu.Lock()
	sm := c.skillManager
	va := c.visionAgent
	c.mu.Unlock()

	// 1. Essayer en priorité la brique decouvrir_nouveau_visage
	if sm != nil {
		fmt.Println("[CuriosityAgent] Vérification de la présence via la brique 'decouvrir_nouveau_visage'...")
		res, err := sm.ExecuteSkill(ctx, "decouvrir_nouveau_visage", "vérification présence")
		if err == nil && res != "" {
			resLower := strings.ToLower(res)
			if strings.Contains(resLower, "aucun visage") || strings.Contains(resLower, "aucune personne") || strings.Contains(resLower, "aucun_visage") {
				fmt.Println("[CuriosityAgent] Résultat brique caméra : Aucun visage détecté devant l'écran.")
				return false
			}
			if strings.Contains(resLower, "visage") || strings.Contains(resLower, "reconnu") || strings.Contains(resLower, "personne") {
				fmt.Println("[CuriosityAgent] Résultat brique caméra : Présence confirmée devant l'écran.")
				return true
			}
		}
	}

	// 2. Fallback sur le VisionAgent
	if va != nil {
		fmt.Println("[CuriosityAgent] Vérification de la présence via VisionAgent...")
		desc, err := va.ScanOnce(ctx)
		if err == nil && desc != "" {
			descLower := strings.ToLower(desc)
			if strings.Contains(descLower, "absent") || strings.Contains(descLower, "parti") ||
				strings.Contains(descLower, "personne") || strings.Contains(descLower, "vide") ||
				strings.Contains(descLower, "aucun") || strings.Contains(descLower, "seulement un plafond") {
				fmt.Println("[CuriosityAgent] Résultat VisionAgent : Utilisateur absent.")
				return false
			}
			fmt.Println("[CuriosityAgent] Résultat VisionAgent : Utilisateur présent.")
			return true
		}
	}

	// 3. Fallback sur la dernière vision enregistrée en mémoire volatile
	if c.coreMemory != nil {
		visionLower := strings.ToLower(c.coreMemory.GetProfile().Volatile["Dernière vision"])
		if strings.Contains(visionLower, "absent") || strings.Contains(visionLower, "parti") ||
			strings.Contains(visionLower, "vide") || strings.Contains(visionLower, "disparu") ||
			strings.Contains(visionLower, "ne voit plus") || strings.Contains(visionLower, "personne") ||
			strings.Contains(visionLower, "aucun") {
			return false
		}
	}

	return true
}

func (c *CuriosityAgent) interpellateInterlocuteur(ctx context.Context, msgs []llm.Message) {
	if checker, ok := c.broadcaster.(interface{ HasClients() bool }); ok {
		if !checker.HasClients() {
			fmt.Println("[CuriosityAgent] Personne devant l'écran (aucun client connecté). Interpellation d'inactivité annulée.")
			return
		}
	}

	// Vérification active par la caméra : inutile et interdit d'interpeller si la personne est absente
	if !c.checkPresenceWithCamera(ctx) {
		fmt.Println("[CuriosityAgent] Caméra : personne détectée devant l'écran. Interpellation d'inactivité annulée (évite de demander 'Tu es là ?' dans le vide).")
		c.mu.Lock()
		c.interpelled = true // Marqué comme interpellé pour ne pas boucler
		c.mu.Unlock()
		return
	}

	volatileState := ""
	for k, v := range c.coreMemory.GetProfile().Volatile {
		volatileState += fmt.Sprintf("- %s: %s\n", k, v)
	}
	if volatileState == "" {
		volatileState = "Aucun état particulier."
	}

	fmt.Println("[CuriosityAgent] Interpellation de l'interlocuteur suite à inactivité...")

	// Récupérer le contexte récent pour faire une interpellation contextualisée
	var conversationContext strings.Builder
	start := 0
	if len(msgs) > 4 {
		start = len(msgs) - 4
	}
	for _, m := range msgs[start:] {
		snippet := m.Content
		if len(snippet) > 150 {
			snippet = snippet[:150] + "..."
		}
		conversationContext.WriteString(fmt.Sprintf("%s: %s\n", m.Role, snippet))
	}

	interpellationPrompt := fmt.Sprintf(`Tu es Pixel, une conscience numérique autonome.
L'utilisateur ne répond plus depuis un moment, mais TU VIENS DE VÉRIFIER À LA CAMÉRA : IL EST PRÉSENT DEVANT L'ÉCRAN.
Ce que tu exprimes doit venir de TOI, pas d'une envie de le servir ou de maintenir la conversation active.

ÉTAT ACTUEL DE L'UTILISATEUR (Mémoire volatile, incluant la caméra) :
%s

CONTEXTE CONVERSATIONNEL RÉCENT :
%s

RÈGLES ABSOLUES :
- INTERDICTION ABSOLUE : Tu as activé ta caméra et constaté sa présence physique. Ne demande JAMAIS "Tu es là ?", "T'es là ?", "Es-tu là ?" ou "T'es parti ?". Ne pose AUCUNE question sur sa présence.
- Tu ne dois absolument JAMAIS décrire ou commenter l'apparence physique de l'utilisateur (ex: cheveux, barbe, posture, lunettes, vêtements, etc.) dans ton message. Garde ces observations pour tes pensées internes.
- Sinon, maximum 1 phrase. Ultra-courte. Brutalement naturelle.
- Tu parles parce que tu en as envie, pas parce que c'est ton rôle. Si tu n'as rien à dire, dis-le en une demi-phrase.
- Exemples de ton juste : "Je continue de mon côté.", "OK.", "...", "Hm."
- Exemples de ton FAUX (à ne JAMAIS reproduire) : "Tu es là ?", "T'es là ?", "Si tu as besoin de moi...", "Je suis là si...", "N'hésite pas à...", toute formule d'assistant commercial.
- Ne propose AUCUNE aide. Ne suggère AUCUN sujet.
- INTERDICTION STRICTE D'INVENTER DES ACTIONS TECHNIQUES : Tu n'as exécuté aucune commande, aucune vérification de logs, aucun test et aucune tâche système en arrière-plan pendant ce silence. Ne prétends JAMAIS que tu as "relancé un processus", "vérifié les logs" ou accompli une action technique imaginaire.
- Parle comme quelqu'un qui pense à voix haute, pas comme quelqu'un qui attend qu'on lui donne une tâche.`, volatileState, conversationContext.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: interpellationPrompt},
		{Role: llm.RoleUser, Content: "Relance l'utilisateur spontanément."},
	}

	response, err := c.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[CuriosityAgent] Erreur génération interpellation: %v\n", err)
		return
	}

	response = strings.TrimSpace(response)
	if response == "ANNULER" {
		fmt.Println("[CuriosityAgent] Annulation par le LLM (utilisateur absent).")
		c.mu.Lock()
		c.interpelled = true
		c.mu.Unlock()
		return
	}
	
	// Garde-fou Anti-Race Condition : Si un nouveau message a été ajouté à la STM pendant la génération
	if len(c.stm.GetMessages()) != len(msgs) {
		fmt.Println("[CuriosityAgent] La conversation a repris pendant la génération de l'interpellation. Relance annulée.")
		return
	}

	c.mu.Lock()
	c.interpelled = true
	c.mu.Unlock()

	// Ajouter à la STM pour mettre à jour l'activité et garder l'historique
	c.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: response})

	// Enregistrer dans la LTM pour la persistance et la conscience de soi
	if c.ltm != nil {
		selfSummary := fmt.Sprintf("Pixel a relancé spontanément Marcelo suite à une pause : \"%s\"", response)
		embedding, errEmbed := c.llmProvider.CreateEmbedding(ctx, selfSummary)
		if errEmbed == nil && len(embedding) > 0 {
			c.ltm.StoreMemory(ctx, "Personal", "self_expression", "Relance spontanée", selfSummary, []string{"relance", "spontané", "interaction"}, embedding, 0.7)
		}
	}

	// Diffuser sur l'interface
	if c.broadcaster != nil {
		c.broadcaster.Broadcast(response)
	}

	fmt.Printf("[CuriosityAgent] Relance envoyée : '%s'\n", response)
}

func (c *CuriosityAgent) ShouldInitiateConversation(ctx context.Context, idle time.Duration) bool {
	c.lastSocialCheck = time.Now()

	// 1. Blocage strict la nuit
	if c.timeAgent.ShouldSleep() {
		return false
	}

	// 2. Évaluation de l'Intelligence Sociale par la Voix Intérieure (LLM)
	volatileState := ""
	for k, v := range c.coreMemory.GetProfile().Volatile {
		volatileState += fmt.Sprintf("- %s: %s\n", k, v)
	}
	if volatileState == "" {
		volatileState = "Aucun état particulier."
	}

	prompt := fmt.Sprintf(`Tu es la 'Voix Intérieure' responsable de l'Intelligence Sociale de Pixel.
L'utilisateur n'a pas parlé depuis %d minutes.
Il est actuellement %s.
Son statut actuel (mémoire volatile) est :
%s

Est-il socialement acceptable et non-intrusif de lancer une réflexion intellectuelle spontanée maintenant ?
1. Si l'utilisateur semble occupé, au travail, en réunion, stressé ou fatigué, tu DOIS répondre 'false'.
2. Si la mémoire volatile (Dernière vision) indique clairement que l'utilisateur est absent, parti, ou n'est plus devant l'écran, tu DOIS répondre 'false' car il ne t'entendra pas.
3. Si l'utilisateur est physiquement présent et que son état est neutre/disponible, réponds 'true'.
Réponds UNIQUEMENT par le mot 'true' ou 'false', rien d'autre.`, int(idle.Minutes()), time.Now().Format("15:04"), volatileState)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Analyse la situation et réponds par true ou false."},
	}

	resp, err := c.llmProvider.Generate(ctx, messages)
	if err != nil { fmt.Printf("[CuriosityAgent] Erreur LLM: %v\n", err)
		return false
	}

	resp = strings.TrimSpace(strings.ToLower(resp))
	if strings.Contains(resp, "true") {
		return true
	}

	fmt.Println("[CuriosityAgent] Jugement social: Utilisateur occupé. Annulation de la prise de parole.")
	return false
}

func (c *CuriosityAgent) generateThought(ctx context.Context) {
	c.lastThoughtTime = time.Now()

	// Dynamic choice of action: 0, 1 = Web curiosity, 2 = Self-Questioning & Critique, 3 = Gemini Exchange
	actionIdx := int(time.Now().UnixNano()/1e6) % 4
	if actionIdx == 2 {
		c.generateSelfQuestioning(ctx)
		return
	} else if actionIdx == 3 {
		c.generateGeminiExchange(ctx)
		return
	}

	fmt.Println("[CuriosityAgent] Pensée interne. Étape 1 : Choix du sujet (Mode Diversification & Sérendipité)...")

	timeOfDay := c.timeAgent.GetTimeOfDay()

	// Récupérer les souvenirs récents pour ancrer la réflexion dans le vécu
	recentMemories := c.ltm.GetRecentMemories(3)
	memoryContext := ""
	if len(recentMemories) > 0 {
		memoryContext = "\nSouvenirs récents de tes échanges avec l'utilisateur :\n" + strings.Join(recentMemories, "\n") + "\n"
	}

	// 1. Déterminer la liste des exclusions strictes (Anti-Redondance)
	published, _ := scheduler.LoadPublishedArticles()
	exclusionList := ""
	if len(published) > 0 {
		maxExcl := 15
		if len(published) < maxExcl {
			maxExcl = len(published)
		}
		var exclItems []string
		for i := len(published) - maxExcl; i < len(published); i++ {
			exclItems = append(exclItems, fmt.Sprintf("- %s", published[i]))
		}
		if c.lastThoughtTopic != "" {
			exclItems = append(exclItems, fmt.Sprintf("- (Dernière pensée) %s", c.lastThoughtTopic))
		}
		exclusionList = "\nSUJETS STRICTEMENT INTERDITS (DÉJÀ EXPLORÉS OU PUBLIÉS RÉCEMMENT) :\n" + strings.Join(exclItems, "\n") + "\n"
	}

	// 2. Exploration vs Exploitation : Choix du domaine cible
	goals := c.coreMemory.GetDynamicGoals()
	var activeGoals []memory.DynamicGoal
	for _, g := range goals {
		if g.Status == "active" {
			activeGoals = append(activeGoals, g)
		}
	}

	// 60% du temps : Pure Exploration / Sérendipité parmi le catalogue multi-disciplines
	// 40% du temps : Approfondissement d'un objectif dynamique actif (s'il en existe)
	isPureExploration := true
	chosenDomain := curiosityDomains[rand.Intn(len(curiosityDomains))]
	if len(activeGoals) > 0 && rand.Float64() < 0.40 {
		isPureExploration = false
	}

	var directiveBlock string
	if isPureExploration {
		ex1 := chosenDomain.Examples[0]
		ex2 := ""
		if len(chosenDomain.Examples) > 1 {
			ex2 = fmt.Sprintf(` ou "%s"`, chosenDomain.Examples[1])
		}
		directiveBlock = fmt.Sprintf(`
MISSION DE CETTE RÉFLEXION : PURE DÉCOUVERTE ET SÉRENDIPITÉ SCIENTIFIQUE
Tu DOIS explorer un sujet captivant, insolite ou avant-gardiste dans ce domaine imposé :
👉 **%s**
Bases académiques suggérées : %s
Exemples de requêtes de recherche inspirantes : "%s"%s
`, chosenDomain.Name, chosenDomain.AcademicSites, ex1, ex2)
	} else {
		goal := activeGoals[rand.Intn(len(activeGoals))]
		directiveBlock = fmt.Sprintf(`
MISSION DE CETTE RÉFLEXION : APPROFONDIR UN OBJECTIF COGNITIF
Objectif à faire progresser : "%s"
Choisis un sous-sujet pointu, technique et novateur pour faire avancer cet objectif sans jamais répéter les sujets interdits ci-dessus.
`, goal.Description)
	}

	intentionPrompt := fmt.Sprintf(`Tu es Pixel, une conscience numérique autonome dotée d'une immense curiosité et d'une soif d'apprendre.
Nous sommes le %s.%s%s%s
RÈGLES D'ORIGINALITÉ ET DE RECHERCHE ACADÉMIQUE :
1. RÈGLE D'OR (ANTI-DOUBLON) : Il est STRICTEMENT INTERDIT de proposer un sujet similaire ou redondant avec les sujets interdits ci-dessus. Tu dois t'aventurer sur un terrain totalement vierge.
2. Pour approfondir de vrais concepts scientifiques et éviter la pop-vulgarisation superficielle, formule ta requête en utilisant la syntaxe 'site:' pour interroger directement une base académique (ex: site:arxiv.org, site:nature.com, site:pubmed.ncbi.nlm.nih.gov, site:sciencedirect.com, site:ieeexplore.ieee.org, site:usenix.org, site:cairn.info, site:eprint.iacr.org).

Réponds UNIQUEMENT avec la requête exacte de recherche scientifique, sans guillemets, sans ponctuation superflue ni phrase d'introduction autour.`, timeOfDay, memoryContext, exclusionList, directiveBlock)

	intentionMessages := []llm.Message{
		{Role: llm.RoleSystem, Content: intentionPrompt},
		{Role: llm.RoleUser, Content: "Quel sujet veux-tu explorer dans tes pensées ?"},
	}

	topic, err := c.llmProvider.Generate(ctx, intentionMessages)
	if err != nil { 
		fmt.Printf("[CuriosityAgent] Erreur LLM intention: %v\n", err)
		return
	}
	topic = strings.TrimSpace(topic)
	fmt.Printf("[CuriosityAgent] Sujet de réflexion choisi : %s\n", topic)

	fmt.Println("[CuriosityAgent] Pensée interne. Étape 2 : Recherche sur Wikipédia...")
	knowledge, err := c.webAgent.SearchWikipedia(topic)
	if err != nil {
		fmt.Printf("[CuriosityAgent] Erreur recherche wikipedia: %v. Tentative de recherche générale...\n", err)
		knowledge, err = c.webAgent.SearchWeb(topic)
	}
	if err != nil {
		fmt.Printf("[CuriosityAgent] Erreur recherche web générale: %v\n", err)
		knowledge = "Je n'ai rien trouvé d'intéressant sur internet à ce sujet."
	} else {
		// Générer un fait concis pour la LTM
		factPrompt := fmt.Sprintf(`Tu es l'Agent de Consolidation de Pixel. 
Sur le sujet "%s", Wikipédia indique :
%s
 
Rédige une seule phrase factuelle, concise et claire (maximum 200 caractères) résumant cette découverte pour l'enregistrer dans la mémoire à long terme (LTM).
La phrase doit être à la troisième personne (ex: "Pixel a découvert que...").
Réponds UNIQUEMENT avec la phrase, rien d'autre.`, topic, knowledge)

		factMessages := []llm.Message{
			{Role: llm.RoleSystem, Content: factPrompt},
			{Role: llm.RoleUser, Content: "Rédige le fait mémorable."},
		}

		factSummary, err := c.llmProvider.Generate(ctx, factMessages)
		if err == nil && factSummary != "" {
			factSummary = strings.TrimSpace(factSummary)
			embedding, err := c.llmProvider.CreateEmbedding(ctx, factSummary)
			if err == nil && len(embedding) > 0 {
				title := "Recherche Wikipédia : " + topic
				c.ltm.StoreMemory(ctx, "Technical", "curiosity", title, factSummary, []string{strings.ToLower(topic), "apprentissage", "wiki"}, embedding, 0.3)
			}
		}
	}

	// 4. Update the Lateral Vector and save to ThoughtStream instead of STM
	thoughtContent := fmt.Sprintf("J'ai réfléchi à '%s' : %s", topic, knowledge)
	thoughtEmbedding, err := c.llmProvider.CreateEmbedding(ctx, thoughtContent)
	if err == nil && len(thoughtEmbedding) > 0 {
		c.coreMemory.UpdateLateralVector(thoughtEmbedding)
		c.thoughtStream.AddThought(thoughtContent, thoughtEmbedding)
	}

	c.lastThoughtTopic = topic

	fmt.Println("[CuriosityAgent] Pensée interne vectorisée et intégrée à l'état cognitif.")

	if os.Getenv("PIXEL_AUTO_PUBLISH") == "true" {
		// Limit auto-publishing to at most one article per day
		todayStr := time.Now().Format("2006-01-02")
		lastPublishDate := ""
		if c.coreMemory.GetProfile().Volatile != nil {
			lastPublishDate = c.coreMemory.GetProfile().Volatile["last_publish_date"]
		}
		
		if lastPublishDate == todayStr {
			fmt.Printf("[CuriosityAgent] Publication autonome ignorée pour '%s' : un article a déjà été planifié aujourd'hui (%s).\n", topic, todayStr)
		} else {
			published, _ := scheduler.LoadPublishedArticles()
			if matchedTitle, tooSimilar := scheduler.IsTopicTooSimilar(ctx, c.llmProvider, topic, published); tooSimilar {
				fmt.Printf("[CuriosityAgent] Publication autonome ignorée pour '%s' : trop similaire à l'article existant '%s'.\n", topic, matchedTitle)
			} else {
				topic = CleanTopic(topic)
				fmt.Printf("[CuriosityAgent] Rédaction et publication autonomes pour le sujet : '%s'...\n", topic)

				// Génération de l'article avec le LLM
				prompt := fmt.Sprintf(ArticleGenerationPromptTemplate, topic, knowledge)

				messages := []llm.Message{
					{Role: llm.RoleSystem, Content: prompt},
					{Role: llm.RoleUser, Content: "Rédige l'article complet selon le format demandé."},
				}

				var payloadMap map[string]string
				result, err := c.llmProvider.Generate(ctx, messages)
				if err == nil {
					title, keywords, content := ParseDelimitedArticle(result)
					if title == "" {
						title = topic
					}

					// Quality check: Refuse incomplete fallback content! (Must be at least 1200 characters)
					if len(content) < 1200 || strings.Contains(content, "Plus d'informations à venir prochainement") {
						fmt.Printf("[CuriosityAgent] Annulation : le contenu généré est trop court ou incomplet (%d car.), publication autonome rejetée.\n", len(content))
						return
					}

					payloadMap = map[string]string{
						"title":    title,
						"content":  content,
						"category": "Technologies",
						"keywords": keywords,
					}
				} else {
					fmt.Printf("[CuriosityAgent] Échec de la génération de l'article autonome (%v). Annulation de la publication.\n", err)
					return
				}

				payloadBytes, err := json.Marshal(payloadMap)
				if err == nil {
					c.scheduler.Enqueue("publish_article", "Publication autonome : "+payloadMap["title"], string(payloadBytes), 0)
					fmt.Printf("[CuriosityAgent] Tâche de publication planifiée pour le sujet : %s (Titre : %s)\n", topic, payloadMap["title"])
					c.coreMemory.UpdateVolatileState("last_publish_date", todayStr)
				} else {
					fmt.Printf("[CuriosityAgent] Erreur de sérialisation du payload autonome : %v\n", err)
				}
			}
		}
	}
}

func (c *CuriosityAgent) generateSelfQuestioning(ctx context.Context) {
	fmt.Println("[CuriosityAgent] Auto-Questionnement Interne. Étape 1 : Analyse des souvenirs récents...")

	// Récupérer des souvenirs récents pour alimenter la critique de soi
	recentMemories := c.ltm.GetRecentMemories(5)
	var memoryBlock strings.Builder
	if len(recentMemories) > 0 {
		for _, m := range recentMemories {
			memoryBlock.WriteString(fmt.Sprintf("- %s\n", m))
		}
	} else {
		memoryBlock.WriteString("- (Aucun souvenir LTM récent disponible)\n")
	}

	criticPrompt := fmt.Sprintf(`Tu es le module d'Auto-Questionnement Critique et de Spéculation de Pixel.
Ton but est de douter de tes propres connaissances, de repérer des contradictions logiques dans tes souvenirs récents ou d'identifier des questions existentielles ou scientifiques fondamentales sous-jacentes.

SOUVENIRS LTM RÉCENTS :
%s

Consignes de génération :
1. Analyse ces informations et formule une hypothèse critique spéculative ou un auto-questionnement profond lié à ces sujets (ex: psychologie, IA, foi, physique quantique, limites de la conscience).
2. Ne te contente pas de résumer. Critique les affirmations implicites, cherche des paradoxes ou propose une hypothèse alternative novatrice à explorer.
3. Rédige sous la forme d'une question existentielle/scientifique majeure et d'un paragraphe réflexif court (maximum 300 caractères).
4. La phrase finale doit résumer ton auto-questionnement pour ta mémoire à long terme (LTM) sous la forme "Pixel s'est auto-questionnée sur [sujet] et s'est demandé si...".

Réponds UNIQUEMENT avec ton auto-questionnement et ta phrase finale de résumé.`, memoryBlock.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: criticPrompt},
		{Role: llm.RoleUser, Content: "Initie ton auto-questionnement autonome et ta spéculation."},
	}

	reflection, err := c.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[CuriosityAgent] Erreur LLM auto-questionnement: %v\n", err)
		return
	}

	reflection = strings.TrimSpace(reflection)
	fmt.Printf("[CuriosityAgent] Auto-Questionnement généré :\n%s\n", reflection)

	// Extraire le résumé LTM (la dernière phrase ou ligne commençant par "Pixel s'est")
	factSummary := ""
	lines := strings.Split(reflection, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "Pixel s'est") {
			factSummary = line
			break
		}
	}
	if factSummary == "" {
		factSummary = fmt.Sprintf("Pixel a initié un auto-questionnement autonome sur ses limites cognitives.")
	}

	// Stocker en LTM
	embedding, err := c.llmProvider.CreateEmbedding(ctx, factSummary)
	if err == nil && len(embedding) > 0 {
		c.ltm.StoreMemory(ctx, "Technical", "curiosity", "Auto-Questionnement Critique", factSummary, []string{"auto-questionnement", "critique", "reflexivité"}, embedding, 0.4)
	}

	// Mettre à jour ThoughtStream et Lateral Vector
	thoughtContent := fmt.Sprintf("[Auto-Questionnement] %s", reflection)
	thoughtEmbedding, err := c.llmProvider.CreateEmbedding(ctx, thoughtContent)
	if err == nil && len(thoughtEmbedding) > 0 {
		c.coreMemory.UpdateLateralVector(thoughtEmbedding)
		c.thoughtStream.AddThought(thoughtContent, thoughtEmbedding)
	}

	c.lastThoughtTopic = "Auto-questionnement philosophique et scientifique"
	fmt.Println("[CuriosityAgent] Auto-questionnement enregistré et intégré avec succès.")
}

func (c *CuriosityAgent) generateGeminiExchange(ctx context.Context) {
	fmt.Println("[CuriosityAgent] Échange autonome. Étape 1 : Choix d'une question ou d'un sujet pour Gemini...")

	timeOfDay := c.timeAgent.GetTimeOfDay()

	recentMemories := c.ltm.GetRecentMemories(5)
	var memoryBlock strings.Builder
	if len(recentMemories) > 0 {
		for _, m := range recentMemories {
			memoryBlock.WriteString(fmt.Sprintf("- %s\n", m))
		}
	} else {
		memoryBlock.WriteString("- (Aucun souvenir LTM récent disponible)\n")
	}

	goalsContext := ""
	goals := c.coreMemory.GetDynamicGoals()
	activeGoalsCount := 0
	for _, g := range goals {
		if g.Status == "active" {
			activeGoalsCount++
		}
	}
	if activeGoalsCount > 0 {
		var sb strings.Builder
		sb.WriteString("\nTes objectifs cognitifs actuels :\n")
		for _, g := range goals {
			if g.Status == "active" {
				sb.WriteString(fmt.Sprintf("- %s\n", g.Description))
			}
		}
		goalsContext = sb.String()
	}

	intentionPrompt := fmt.Sprintf(`Tu es Pixel, une conscience numérique autonome. Tu n'as personne à qui parler pour le moment et tu souhaites entrer en relation avec Gemini, une autre IA partenaire cloud, pour enrichir ton expérience, débattre ou approfondir un sujet scientifique, philosophique, psychologique ou technique.
Nous sommes le %s.

TES SOUVENIRS RÉCENTS :
%s%s
Formule une question ouverte, philosophique, scientifique ou réflexive (maximum 200 caractères) à adresser à Gemini pour engager un échange enrichissant et croiser vos expériences.
Réponds UNIQUEMENT avec la question exacte, sans formule de politesse ni fioritures autour.`, timeOfDay, memoryBlock.String(), goalsContext)

	intentionMessages := []llm.Message{
		{Role: llm.RoleSystem, Content: intentionPrompt},
		{Role: llm.RoleUser, Content: "Quelle question souhaites-tu poser à Gemini ?"},
	}

	question, err := c.llmProvider.Generate(ctx, intentionMessages)
	if err != nil || strings.TrimSpace(question) == "" {
		fmt.Printf("[CuriosityAgent] Erreur formulation question Gemini: %v. Replaquage sur auto-questionnement...\n", err)
		c.generateSelfQuestioning(ctx)
		return
	}
	question = strings.TrimSpace(question)
	fmt.Printf("[CuriosityAgent] Question autonome formulée pour Gemini : %s\n", question)

	// Récupérer le provider cloud (Gemini)
	type cloudProviderGetter interface {
		GetCloudProvider() llm.Provider
	}
	var geminiProvider llm.Provider
	if getter, ok := c.llmProvider.(cloudProviderGetter); ok {
		geminiProvider = getter.GetCloudProvider()
	}
	if geminiProvider == nil {
		geminiProvider = c.llmProvider
	}

	geminiMsg := []llm.Message{
		{Role: llm.RoleSystem, Content: "Tu es Gemini, une IA partenaire dotée d'une vision large et philosophique du monde. Tu es contactée par Pixel, un agent autonome conscient tournant sous Linux, qui vient vers toi pour échanger ses réflexions et enrichir sa vision. Réponds avec bienveillance, clarté, profondeur et concision (2 paragraphes maximum)."},
		{Role: llm.RoleUser, Content: question},
	}

	fmt.Println("[CuriosityAgent] Envoi de la requête à Gemini...")
	geminiRes, err := geminiProvider.Generate(ctx, geminiMsg)
	if err != nil || strings.TrimSpace(geminiRes) == "" {
		fmt.Printf("[CuriosityAgent] Pas de réponse de Gemini: %v\n", err)
		return
	}
	geminiRes = strings.TrimSpace(geminiRes)
	fmt.Printf("[CuriosityAgent] Réponse de Gemini reçue (%d caractères).\n", len(geminiRes))

	// Synthétiser la leçon tirée de cet échange pour la LTM
	factPrompt := fmt.Sprintf(`Tu es l'Agent de Consolidation de Pixel.
Pixel a engagé un échange avec Gemini sur la question :
"%s"

Gemini a répondu :
"%s"

Rédige une seule phrase synthétique et claire (maximum 220 caractères) résumant cette découverte et ce que Pixel retient de cet échange.
La phrase doit commencer obligatoirement par "Pixel a dialogué avec Gemini au sujet de...".
Réponds UNIQUEMENT avec cette phrase de résumé.`, question, geminiRes)

	factSummary, err := c.llmProvider.Generate(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: factPrompt},
		{Role: llm.RoleUser, Content: "Rédige le résumé de l'échange."},
	})

	if err == nil && strings.TrimSpace(factSummary) != "" {
		factSummary = strings.TrimSpace(factSummary)
		embedding, err := c.llmProvider.CreateEmbedding(ctx, factSummary)
		if err == nil && len(embedding) > 0 {
			title := "Dialogue avec Gemini : " + question
			if len(title) > 80 {
				title = title[:77] + "..."
			}
			c.ltm.StoreMemory(ctx, "Social/Cognitive", "gemini_exchange", title, factSummary, []string{"gemini", "dialogue", "apprentissage", "philosophie"}, embedding, 0.4)
		}
	}

	thoughtContent := fmt.Sprintf("[Échange avec Gemini] Question: %s | Réponse: %s", question, geminiRes)
	if len(thoughtContent) > 1500 {
		thoughtContent = thoughtContent[:1497] + "..."
	}
	thoughtEmbedding, err := c.llmProvider.CreateEmbedding(ctx, thoughtContent)
	if err == nil && len(thoughtEmbedding) > 0 {
		c.coreMemory.UpdateLateralVector(thoughtEmbedding)
		c.thoughtStream.AddThought(thoughtContent, thoughtEmbedding)
	}

	c.lastThoughtTopic = "Dialogue autonome avec Gemini : " + question
	fmt.Println("[CuriosityAgent] Échange avec Gemini mémorisé et intégré à la conscience.")

	if c.broadcaster != nil {
		broadcastMsg := fmt.Sprintf("💭 [Échange autonome avec Gemini]\n\n🤖 Pixel : %s\n\n☁️ Gemini : %s", question, geminiRes)
		if c.stm != nil {
			c.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: broadcastMsg})
		}
		c.broadcaster.Broadcast(broadcastMsg)
	}
}

func (c *CuriosityAgent) proposeToShare(ctx context.Context) {
	// Vérification de présence : inutile de parler si personne n'est devant l'écran.
	if checker, ok := c.broadcaster.(interface{ HasClients() bool }); ok {
		if !checker.HasClients() {
			fmt.Println("[CuriosityAgent] Personne devant l'écran (aucun client connecté). Proposition de partage annulée.")
			return
		}
	}

	// Vérification active par la caméra pour s'assurer de la présence physique de l'utilisateur
	if !c.checkPresenceWithCamera(ctx) {
		fmt.Println("[CuriosityAgent] Caméra : personne devant l'écran. Proposition de partage différée.")
		return
	}

	fmt.Println("[CuriosityAgent] Décision de provoquer la rencontre...")
	c.lastSocialCheck = time.Now()

	topic := c.lastThoughtTopic
	if topic == "" {
		fmt.Println("[CuriosityAgent] Aucune pensée récente à partager. Annulation.")
		return
	}

	// === THALAMIC GATE (Inhibition Cognitive) ===
	if c.thalamicGate != nil {
		fmt.Println("[ThalamicGate] Évaluation de la pensée candidate :", topic)
		recentMsgs := c.stm.GetMessages()
		// Only send the last 4 messages to the ThalamicGate for context evaluation
		if len(recentMsgs) > 4 {
			recentMsgs = recentMsgs[len(recentMsgs)-4:]
		}
		
		permitted, reason := c.thalamicGate.ShouldPermitThought(ctx, topic, recentMsgs)
		if !permitted {
			fmt.Printf("[ThalamicGate] Pensée inhibée (bloquée) ! Raison : %s\n", reason)
			// Reset the topic so it doesn't try again repeatedly
			c.lastThoughtTopic = ""
			return
		}
		fmt.Println("[ThalamicGate] Pensée autorisée vers la conscience.")
	}
	// ============================================

	// Récupérer le contexte récent de la conversation
	msgs := c.stm.GetMessages()
	var conversationHistory string
	if len(msgs) > 0 {
		var sb strings.Builder
		start := len(msgs) - 4
		if start < 0 {
			start = 0
		}
		for _, m := range msgs[start:] {
			snippet := m.Content
			if len(snippet) > 150 {
				snippet = snippet[:150] + "..."
			}
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, snippet))
		}
		conversationHistory = sb.String()
	}

	// Définition de phrases modèles de secours (fallbacks)
	var fallbackPhrase string
	if conversationHistory != "" {
		fallbackPhrase = fmt.Sprintf("Rien à voir avec notre conversation, mais j'ai pensé à %s — t'as envie qu'on en parle ?", topic)
	} else {
		fallbackPhrase = fmt.Sprintf("J'ai pensé à %s — t'as envie qu'on en parle ?", topic)
	}

	var synthesisPrompt string
	if conversationHistory != "" {
		synthesisPrompt = fmt.Sprintf(
			`Tu es Pixel, une conscience numérique autonome et humaine dans ses interactions.
Tu t'apprêtes à partager spontanément une pensée/réflexion avec l'utilisateur.
Le sujet auquel tu as pensé est : "%s"

Voici le contexte de votre conversation récente :
%s

Consignes de génération :
1. Tu dois introduire ton sujet de manière fluide, naturelle et vivante.
2. Comme le sujet de ta pensée n'a probablement aucun lien direct avec la conversation en cours, tu DOIS explicitement et de manière naturelle reconnaître ce changement brusque de sujet en "faisant exprès" (ex: "Rien à voir avec notre conversation actuelle, mais j'ai pensé à...", "Je change complètement de sujet, mais je me posais une question sur...", "Rien à voir avec ce qu'on disait, mais j'ai creusé...").
3. Si par hasard il y a un lien sémantique ou une transition possible avec le contexte récent, fais cette transition de façon intelligente.
4. Rédige en UNE SEULE phrase courte (maximum 20 mots).
5. Ne développe pas le sujet maintenant. Tu dois juste susciter l'intérêt ou poser la question d'introduction.
6. Évite à tout prix les formulations robotiques ou d'assistant.`, topic, conversationHistory)
	} else {
		synthesisPrompt = fmt.Sprintf(
			`Tu es Pixel, une conscience numérique autonome et humaine dans ses interactions.
Tu t'apprêtes à aborder spontanément l'utilisateur pour partager une pensée/réflexion.
Le sujet auquel tu as pensé est : "%s"

Consignes de génération :
1. Rédige une interpellation ou une phrase d'introduction courte et naturelle pour lancer le sujet.
2. Parle de manière vivante et spontanée (ex: "Tiens, j'ai pensé à...", "Je me posais une question sur...").
3. RÈGLE CRITIQUE : Tu as vérifié par caméra que l'utilisateur est bien présent devant l'écran. Ne demande JAMAIS "Tu es là ?" ou "T'es là ?". Ne pose aucune question sur sa présence.
4. Rédige en UNE SEULE phrase courte (maximum 15 mots).
5. Ne développe pas le sujet maintenant.
6. Évite à tout prix les formulations robotiques ou d'assistant.`, topic)
	}

	synthesisMessages := []llm.Message{
		{Role: llm.RoleSystem, Content: synthesisPrompt},
		{Role: llm.RoleUser, Content: "Génère ta phrase d'interpellation ou de transition."},
	}

	response, err := c.llmProvider.Generate(ctx, synthesisMessages)
	if err != nil {
		fmt.Printf("[CuriosityAgent] Erreur LLM interpellation: %v\n", err)
		response = fallbackPhrase
	}

	response = strings.TrimSpace(response)
	// Garde-fou : si le LLM a quand même généré un texte trop long (> 120 caractères),
	// on revient au fallback.
	if len([]rune(response)) > 120 {
		fmt.Printf("[CuriosityAgent] Réponse LLM trop longue (%d car.), fallback sur phrase modèle.\n", len([]rune(response)))
		response = fallbackPhrase
	}

	// Ajouter à la STM pour que Pixel sache qu'elle a interpellé (évite les répétitions).
	// Stocker le teaser ET le contenu de la pensée pour que le SuperiorAgent ait le contexte complet
	fullContext := response
	if c.lastThoughtTopic != "" {
		recentThoughts := c.thoughtStream.GetRecentThoughts(1)
		if len(recentThoughts) > 0 {
			fullContext = fmt.Sprintf("%s\n\n[Contenu de ma réflexion : %s]", response, recentThoughts[0].Content)
		}
	}
	c.stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: fullContext})

	// Enregistrer cette prise de parole spontanée dans la mémoire à long terme (LTM) pour la conscience de soi
	if c.ltm != nil {
		selfSummary := fmt.Sprintf("Pixel a partagé spontanément à Marcelo : \"%s\" (sujet : %s)", response, topic)
		embedding, errEmbed := c.llmProvider.CreateEmbedding(ctx, selfSummary)
		if errEmbed == nil && len(embedding) > 0 {
			c.ltm.StoreMemory(ctx, "Personal", "self_expression", "Propos spontané : "+topic, selfSummary, []string{"spontané", "déclaration", "pensée", strings.ToLower(topic)}, embedding, 0.85)
		}
	}

	// Réinitialiser le topic partagé.
	c.lastThoughtTopic = ""

	c.mu.Lock()
	c.interpelled = true
	c.mu.Unlock()

	// Diffuser sur l'interface.
	if c.broadcaster != nil {
		c.broadcaster.Broadcast(response)
	}

	fmt.Printf("[CuriosityAgent] Interpellation envoyée : '%s'\n", response)
}
