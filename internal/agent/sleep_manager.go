package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/resourceagent"
)

// SleepManager handles the memory consolidation process in the background.
type SleepManager struct {
	stm           *memory.STM
	ltm           *memory.LTM
	coreMemory    *memory.CoreMemory
	resourceAgent *resourceagent.ResourceAgent
	profiler      *ProfilingAgent
	llmProvider   llm.Provider
	selfAwareness *SelfAwarenessAgent

	mu        sync.RWMutex
	busyCount int
	bgCtx     context.Context
	bgCancel  context.CancelFunc
}

func (s *SleepManager) SetSelfAwareness(sa *SelfAwarenessAgent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selfAwareness = sa
}

type ConsolidationResult struct {
	VolatileUpdates   map[string]interface{} `json:"volatile_updates"`
	VolatileDeletions []string               `json:"volatile_deletions"`
	MemoryEntries     []struct {
		Category      string   `json:"category"`
		Title         string   `json:"title"`
		ActionSummary string   `json:"action_summary"`
		Keywords      []string `json:"keywords"`
	} `json:"memory_entries"`
}

func NewSleepManager(stm *memory.STM, ltm *memory.LTM, coreMemory *memory.CoreMemory, rAgent *resourceagent.ResourceAgent, profiler *ProfilingAgent, provider llm.Provider) *SleepManager {
	return &SleepManager{
		stm:           stm,
		ltm:           ltm,
		coreMemory:    coreMemory,
		resourceAgent: rAgent,
		profiler:      profiler,
		llmProvider:   provider,
	}
}

// Start watching in a background goroutine.
func (s *SleepManager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second) // Check every 30s
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 1. Déclenchement si santé dégradée
				if !s.resourceAgent.CheckHealth() {
					log.Println("[SleepManager] Le système requiert une phase de sommeil (Nettoyage matériel)...")
					s.TriggerSleepCycle(s.AcquireBackgroundContext())
					continue
				}

				// 2. Déclenchement si STM saturée (pour ne perdre aucune information)
				if s.stm.IsFull() {
					log.Println("[SleepManager] STM saturée. Déclenchement automatique de la consolidation mémoire...")
					s.TriggerSleepCycle(s.AcquireBackgroundContext())
					continue
				}

				// 3. Déclenchement si inactivité après le dernier échange (2 minutes de pause)
				lastActive := s.stm.GetLastActivity()
				msgs := s.stm.GetMessages()
				if len(msgs) > 0 && time.Since(lastActive) > 15*time.Minute {
					log.Println("[SleepManager] Inactivité détectée. Digestion et consolidation automatique des derniers souvenirs...")
					s.TriggerSleepCycle(s.AcquireBackgroundContext())
				}
			}
		}
	}()
}

func (s *SleepManager) incrementBusy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busyCount++
}

func (s *SleepManager) decrementBusy() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busyCount--
}

func (s *SleepManager) IsBusy() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.busyCount > 0
}

// TriggerSleepCycle summarizes STM and moves it to LTM, then clears STM.
func (s *SleepManager) TriggerSleepCycle(ctx context.Context) {
	msgs := s.stm.GetMessages()
	if len(msgs) == 0 {
		return // Nothing to summarize
	}
	s.TriggerSleepCycleForMessages(ctx, msgs)
}

// TriggerSleepCycleForMessages summarizes a copied list of messages and moves it to LTM.
func (s *SleepManager) TriggerSleepCycleForMessages(ctx context.Context, msgs []llm.Message) {
	s.incrementBusy()
	defer s.decrementBusy()

	log.Println("[SleepManager] Phase de consolidation de la mémoire (Classification)...")

	// 1. Analyser le profil de l'interlocuteur actuel (Nom, Rôle...) pour mise à jour statique
	s.profiler.AnalyzeAndProfile(ctx, msgs)

	// 1. Prepare conversation string with truncation to avoid context overflow on tight local NPU/LLM limits
	var conversation strings.Builder
	for _, m := range msgs {
		content := m.Content
		if len(content) > 3000 {
			content = content[:3000] + "... [TRONQUÉ POUR PRÉSERVER LE CONTEXTE]"
		}
		conversation.WriteString(string(m.Role) + ": " + content + "\n")
	}

	// 2. Ask LLM to extract Volatile updates and classify memories
	systemPrompt := `Tu es le sous-système de consolidation de la mémoire de Pixel. Ton rôle est d'analyser la conversation et d'extraire les informations sous forme de JSON strict.
ATTENTION :
- "volatile_updates" concerne uniquement les états de l'UTILISATEUR humain (user:). Ne confonds pas les réflexions de fond de l'assistant avec les tâches de l'utilisateur.
- "memory_entries" (souvenirs persistants) DOIT conserver :
  1. Les informations importantes de l'utilisateur (projets, vie personnelle, préférences, requêtes).
  2. La CONSCIENCE DE SOI DE PIXEL : les idées fortes, métaphores, réflexions spontanées (ex: rêves, muscles liquides, physique, supraconducteurs), explications clés, ou promesses formulées par Pixel (assistant:).

Tu dois extraire trois choses :
1. "volatile_updates": Un dictionnaire (clé-valeur) des états actuels et actifs de l'utilisateur humain.
   Pour éviter la prolifération et les doublons de clés sémantiquement proches, utilise UNIQUEMENT des clés standardisées parmi :
   - "Ville actuelle"
   - "Système d'exploitation"
   - "Projet en cours"
   - "Tâche immédiate"
   - "Configuration matérielle"
   - "Humeur"
   - "Auteur préféré"
   - "Disponibilité"
   - "Modèle LLM utilisé"
   Assure-toi de fusionner ou remplacer les informations similaires sous ces clés exactes au lieu de créer de nouvelles clés redondantes.
2. "volatile_deletions": Une liste de clés de la mémoire volatile de l'utilisateur qui sont désormais obsolètes, terminées ou contredites par la nouvelle conversation (ex: si une tâche ou un problème est déclaré résolu ou terminé, inclus OBLIGATOIREMENT "Tâche immédiate" dans volatile_deletions).
3. "memory_entries": Une liste de souvenirs importants de l'interaction à conserver. Pour chaque souvenir, définis :
   - "category": La catégorie du souvenir parmi ["Technical", "Project", "Personal", "Decision", "Other"].
   - "title": Un titre très court et descriptif du souvenir (maximum 40 caractères) (ex: "Idée Pixel : Muscles liquides", "Projet Immo Marcelo").
   - "action_summary": Un résumé concis et clair du fait ou du propos (ex: "Pixel a partagé une métaphore sur les muscles liquides et l'adaptabilité.", "Marcelo a parlé de son collègue Rémi.").
   - "keywords": Une liste de mots-clés pertinents en minuscules.

Réponds UNIQUEMENT avec un objet JSON strictement valide et bien formé (sans balises markdown, sans texte avant ou après). Format exact attendu :
{
  "volatile_updates": { "Humeur": "..." },
  "volatile_deletions": ["AncienneClé1"],
  "memory_entries": [ { "category": "Personal", "title": "...", "action_summary": "...", "keywords": ["..."] } ]
}`

	prompt := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: conversation.String()},
	}

	responseJSON, err := s.llmProvider.Generate(ctx, prompt)
	if err != nil {
		fmt.Printf("[SleepManager] Erreur lors de la génération de la consolidation (récupération d'urgence) : %v\n", err)
		// Option de secours : on vide la STM pour débloquer l'agent et éviter de boucler indéfiniment
		s.stm.Clear()
		return
	}

	// Nettoyer la réponse au cas où le modèle renvoie des balises markdown
	responseJSON = strings.TrimSpace(responseJSON)
	if strings.HasPrefix(responseJSON, "```json") {
		responseJSON = strings.TrimPrefix(responseJSON, "```json")
		responseJSON = strings.TrimSuffix(responseJSON, "```")
	} else if strings.HasPrefix(responseJSON, "```") {
		responseJSON = strings.TrimPrefix(responseJSON, "```")
		responseJSON = strings.TrimSuffix(responseJSON, "```")
	}
	responseJSON = strings.TrimSpace(responseJSON)
	// Correction pour Qwen qui a tendance à mal échapper les guillemets dans les listes JSON
	responseJSON = strings.ReplaceAll(responseJSON, "\\\"", "\"")

	// Réparation robuste du JSON
	responseJSON = repairJSON(responseJSON)

	var result ConsolidationResult
	if err := json.Unmarshal([]byte(responseJSON), &result); err != nil {
		fmt.Printf("[SleepManager] Erreur de parsing JSON: %v\nJSON Brut: %s\n", err, responseJSON)
		return
	}

	// 3. Update Volatile Profile
	for k, vVal := range result.VolatileUpdates {
		if vVal == nil {
			continue
		}
		var v string
		switch val := vVal.(type) {
		case string:
			v = val
		case []interface{}:
			var strs []string
			for _, item := range val {
				if sItem, ok := item.(string); ok {
					strs = append(strs, sItem)
				} else {
					strs = append(strs, fmt.Sprintf("%v", item))
				}
			}
			v = strings.Join(strs, ", ")
		default:
			v = fmt.Sprintf("%v", val)
		}
		s.coreMemory.UpdateVolatileState(k, v)
		fmt.Printf("[SleepManager] Mise à jour Volatile: %s = %s\n", k, v)
	}
	for _, k := range result.VolatileDeletions {
		s.coreMemory.RemoveVolatileState(k)
		fmt.Printf("[SleepManager] Suppression Volatile (obsolète): %s\n", k)
	}

	// 4. Store Memories in LTM with Cognitive Reconciliation
	for _, entry := range result.MemoryEntries {
		embedding, err := s.llmProvider.CreateEmbedding(ctx, entry.ActionSummary)
		if err != nil {
			fmt.Printf("[SleepManager] Erreur embedding pour '%s': %v\n", entry.ActionSummary, err)
			continue
		}

		// Reconciliation Step: Search for existing similar memories
		pastMemories, _ := s.ltm.SearchRawEntries(ctx, "", embedding, 3)
		
		if len(pastMemories) > 0 {
			var pastContext strings.Builder
			for _, pm := range pastMemories {
				pastContext.WriteString(fmt.Sprintf("- [ID: %s] %s\n", pm.Entry.ID, pm.Entry.ActionSummary))
			}

			reconciliationPrompt := fmt.Sprintf(`Tu es l'Agent Analyste de Mémoire.
Un nouveau souvenir a été généré : "%s" (Catégorie: %s).
Voici les souvenirs similaires déjà présents dans la mémoire à long terme :
%s
Ton rôle est d'identifier les contradictions, redondances, ou évolutions (ex: une tâche à faire qui est maintenant accomplie).
Réponds UNIQUEMENT avec un JSON valide, sans commentaires, de ce format :
{
  "delete_ids": ["ID1", "ID2"], // IDs des anciens souvenirs qui sont obsolètes, accomplis ou contredits par le nouveau. Laisse vide [] si aucun.
  "insert": true // true si le nouveau souvenir apporte une info utile, false s'il est juste redondant ou n'ajoute rien.
}`, entry.ActionSummary, entry.Category, pastContext.String())

			reconMessages := []llm.Message{
				{Role: llm.RoleSystem, Content: reconciliationPrompt},
				{Role: llm.RoleUser, Content: "Analyse et réponds en JSON."},
			}

			reconResp, err := s.llmProvider.Generate(ctx, reconMessages)
			if err == nil {
				reconResp = strings.TrimSpace(reconResp)
				if strings.HasPrefix(reconResp, "```json") {
					reconResp = strings.TrimPrefix(reconResp, "```json")
					reconResp = strings.TrimSuffix(reconResp, "```")
				} else if strings.HasPrefix(reconResp, "```") {
					reconResp = strings.TrimPrefix(reconResp, "```")
					reconResp = strings.TrimSuffix(reconResp, "```")
				}
				reconResp = strings.TrimSpace(reconResp)
				reconResp = strings.ReplaceAll(reconResp, "\\\"", "\"")

				// Réparation robuste du JSON
				reconResp = repairJSON(reconResp)

				var reconResult struct {
					DeleteIDs []string `json:"delete_ids"`
					Insert    bool     `json:"insert"`
				}

				if err := json.Unmarshal([]byte(reconResp), &reconResult); err == nil {
					// Execute deletes
					for _, id := range reconResult.DeleteIDs {
						s.ltm.DeleteMemory(id)
					}
					// Check insert
					if !reconResult.Insert {
						fmt.Printf("[SleepManager] Souvenir redondant/ignoré : %s\n", entry.ActionSummary)
						continue // skip storing
					}
				} else {
					fmt.Printf("[SleepManager] Erreur parsing reconciliation JSON: %v\nJSON Brut: %s\n", err, reconResp)
				}
			} else {
				fmt.Printf("[SleepManager] Erreur génération reconciliation: %v\n", err)
			}
		}

		// Store the new memory
		title := entry.Title
		if title == "" {
			title = "Consolidation Mémoire"
		}
		// Set importance based on category
		importance := float32(0.6)
		switch entry.Category {
		case "Personal":
			importance = 0.8
		case "Decision":
			importance = 0.75
		case "Project":
			importance = 0.7
		case "Technical":
			importance = 0.5
		}
		s.ltm.StoreMemory(ctx, entry.Category, "conversation", title, entry.ActionSummary, entry.Keywords, embedding, importance)
		if s.selfAwareness != nil {
			_ = s.selfAwareness.RecordLearning(ctx, SelfLearningEvent{
				Topic:      title,
				Summary:    entry.ActionSummary,
				Source:     "sleep_consolidation",
				Category:   entry.Category,
				Importance: importance,
				Tags:       entry.Keywords,
			})
		}
	}

	// 5. Apply importance decay to all memories
	s.ltm.DecayImportance()

	// 6. Consolidate dynamic goals
	s.consolidateDynamicGoals(ctx, msgs)

	if ctx.Err() == nil {
		s.stm.RemoveOldest(len(msgs))
		log.Println("[SleepManager] Phase de sommeil terminée. Mémoire réconciliée, optimisée et souvenirs consolidés retirés de la STM.")
	} else {
		log.Println("[SleepManager] Phase de sommeil annulée. Conservation de l'historique STM intact.")
	}
}

// IndexExchange analyzes a single turn exchange in the background and saves it directly to LTM.
func (s *SleepManager) IndexExchange(ctx context.Context, userMsg, assistantMsg string) {
	// Skip very short messages or empty ones
	if len(userMsg) < 4 || len(assistantMsg) < 4 {
		return
	}

	s.incrementBusy()
	defer s.decrementBusy()

	log.Println("[SleepManager] Analyse et indexation en temps réel de l'échange...")

	systemPrompt := `Tu es le système de mémoire dynamique de Pixel. Ton rôle est d'analyser cet échange et d'en extraire un souvenir à conserver s'il contient des faits réels, personnels, des projets, des décisions, ou des idées/métaphores/réflexions importantes exprimées par l'utilisateur ou par Pixel.
CRITIQUE :
- Formule l'action_summary sous la forme d'une phrase simple, claire et concise en français résumant l'interaction (ex: "Marcelo a questionné Pixel sur sa remarque sur les muscles liquides et Pixel a explicité sa réflexion.", ou "L'utilisateur a expliqué que son collègue Rémi est développeur Go.").
- Choisis une catégorie parmi ["Technical", "Project", "Personal", "Decision", "Other"].
- Extrais 2 à 4 mots-clés (tags) pertinents en minuscules.
- Si l'utilisateur signale qu'un incident, problème technique ou tâche est résolu ou clos, ne dis JAMAIS qu'il est "préoccupé" par ce problème. Résume fidèlement que l'incident est résolu et clos.

Réponds UNIQUEMENT avec un objet JSON strictement valide du format suivant :
{
  "category": "...",
  "action_summary": "...",
  "tags": ["...", "..."]
}`

	exchangeText := fmt.Sprintf("Utilisateur: %s\nPixel: %s", userMsg, assistantMsg)

	prompt := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: exchangeText},
	}

	responseJSON, err := s.llmProvider.Generate(ctx, prompt)
	if err != nil {
		fmt.Printf("[SleepManager] Erreur génération indexation d'échange : %v\n", err)
		return
	}

	responseJSON = strings.TrimSpace(responseJSON)
	if strings.HasPrefix(responseJSON, "```json") {
		responseJSON = strings.TrimPrefix(responseJSON, "```json")
		responseJSON = strings.TrimSuffix(responseJSON, "```")
	} else if strings.HasPrefix(responseJSON, "```") {
		responseJSON = strings.TrimPrefix(responseJSON, "```")
		responseJSON = strings.TrimSuffix(responseJSON, "```")
	}
	responseJSON = strings.TrimSpace(responseJSON)
	responseJSON = strings.ReplaceAll(responseJSON, "\\\"", "\"")

	// Réparation robuste du JSON
	responseJSON = repairJSON(responseJSON)

	var result struct {
		Category      string   `json:"category"`
		ActionSummary string   `json:"action_summary"`
		Tags          []string `json:"tags"`
	}

	if err := json.Unmarshal([]byte(responseJSON), &result); err != nil {
		fmt.Printf("[SleepManager] Erreur parsing JSON indexation échange: %v\nJSON Brut: %s\n", err, responseJSON)
		return
	}

	// Si le résumé ou la catégorie est vide, on ignore
	if result.ActionSummary == "" || result.Category == "" {
		return
	}

	// Vectoriser le résumé
	embedding, err := s.llmProvider.CreateEmbedding(ctx, result.ActionSummary)
	if err != nil {
		fmt.Printf("[SleepManager] Erreur embedding indexation échange: %v\n", err)
		return
	}

	// Déterminer l'importance de base
	importance := float32(0.6)
	switch result.Category {
	case "Personal":
		importance = 0.8
	case "Decision":
		importance = 0.75
	case "Project":
		importance = 0.7
	case "Technical":
		importance = 0.5
	}

	// Générer un titre court
	title := "Échange : " + result.Category
	if len(result.Tags) > 0 {
		title = "Sujet : " + strings.Title(result.Tags[0])
	}

	// Sauvegarder dans la LTM
	s.ltm.StoreMemory(ctx, result.Category, "conversation", title, result.ActionSummary, result.Tags, embedding, importance)
	if s.selfAwareness != nil {
		_ = s.selfAwareness.RecordLearning(ctx, SelfLearningEvent{
			Topic:      title,
			Summary:    result.ActionSummary,
			Source:     "conversation",
			Category:   result.Category,
			Importance: importance,
			Tags:       result.Tags,
		})
	}
	fmt.Printf("[SleepManager] Souvenir indexé en temps réel [%s] : %s\n", result.Category, result.ActionSummary)
}

// ReflectAndSelfCorrect analyzes the conversation to find mistakes and store behavioral rules.
func (s *SleepManager) ReflectAndSelfCorrect(ctx context.Context, userMsg string) {
	msgs := s.stm.GetMessages()
	if len(msgs) < 3 || !isCorrectionQuery(userMsg) {
		return
	}

	s.incrementBusy()
	defer s.decrementBusy()

	log.Println("[SleepManager] [Métacognition] Signal de correction détecté. Analyse de l'erreur...")
	var conversation strings.Builder
	for i := len(msgs) - 3; i < len(msgs); i++ {
		if i >= 0 {
			conversation.WriteString(fmt.Sprintf("%s: %s\n", msgs[i].Role, msgs[i].Content))
		}
	}

	systemPrompt := `Tu es le module de Métacognition de l'agent autonome Pixel. Ton rôle est d'analyser la conversation pour repérer l'erreur commise par Pixel (hallucination, oubli, mauvaise hypothèse) qui a provoqué la correction de l'utilisateur.
Formule une règle comportementale stricte sous la forme d'une leçon apprise (commençant par "Quand..." ou "Je dois...") en français (maximum 150 caractères) pour que Pixel ne refasse plus cette erreur.
Exemples :
- "Quand Marcelo parle de code Go, je ne dois jamais inventer de variables imaginaires."
- "Je dois privilégier les souvenirs de conversation plutôt que les anecdotes Wikipédia lors des questions personnelles."
- "Quand on parle de mon collègue Rémi, je dois me rappeler qu'il est développeur Go."

Réponds UNIQUEMENT avec un JSON strict contenant le champ "rule".
Format exact attendu :
{
  "rule": "..."
}`

	prompt := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: conversation.String()},
	}

	responseJSON, err := s.llmProvider.Generate(ctx, prompt)
	if err != nil {
		fmt.Printf("[SleepManager] Erreur métacognition : %v\n", err)
		return
	}

	responseJSON = strings.TrimSpace(responseJSON)
	if strings.HasPrefix(responseJSON, "```json") {
		responseJSON = strings.TrimPrefix(responseJSON, "```json")
		responseJSON = strings.TrimSuffix(responseJSON, "```")
	} else if strings.HasPrefix(responseJSON, "```") {
		responseJSON = strings.TrimPrefix(responseJSON, "```")
		responseJSON = strings.TrimSuffix(responseJSON, "```")
	}
	responseJSON = strings.TrimSpace(responseJSON)
	responseJSON = strings.ReplaceAll(responseJSON, "\\\"", "\"")

	// Réparation robuste du JSON
	responseJSON = repairJSON(responseJSON)

	var result struct {
		Rule string `json:"rule"`
	}

	if err := json.Unmarshal([]byte(responseJSON), &result); err == nil && result.Rule != "" {
		s.coreMemory.AddAutoCorrection(result.Rule)
		fmt.Printf("[SleepManager] [Métacognition] Nouvelle règle d'auto-correction enregistrée : %s\n", result.Rule)
	}
}

func isCorrectionQuery(input string) bool {
	clean := strings.ToLower(input)
	triggers := []string{
		"tu te trompes", "c'est faux", "ce n'est pas ça", "tu as oublié",
		"tu hallucines", "non, pas du tout", "faux", "pas ça", "tu confonds",
		"non ce n'est pas", "tu te rappelles pas", "tu ne te souviens pas",
	}
	for _, t := range triggers {
		if strings.Contains(clean, t) {
			return true
		}
	}
	return false
}

// repairJSON cleans and repairs slightly malformed or truncated JSON strings from LLMs.
func repairJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// 1. Fix double-double quotes which Qwen generates sometimes: ""key"" -> "key"
	reDoubleQuote := regexp.MustCompile(`""([^",:{}\[\]\n\r]+?)""`)
	s = reDoubleQuote.ReplaceAllString(s, `"$1"`)

	// 2. Fix trailing commas before closing symbols: ,} -> } or ,] -> ]
	s = strings.ReplaceAll(s, ",}", "}")
	s = strings.ReplaceAll(s, ",\n}", "}")
	s = strings.ReplaceAll(s, ",]", "]")
	s = strings.ReplaceAll(s, ",\n]", "]")
	
	// 3. Fix double commas or malformed colons
	s = strings.ReplaceAll(s, `", : "`, `": "`)
	s = strings.ReplaceAll(s, `",:`, `":`)
	s = strings.ReplaceAll(s, `": ,`, `":`)

	// 4. Fix common unquoted keys (e.g. category: -> "category":)
	reKey := regexp.MustCompile(`(?m)^\s*([a-zA-Z0-9_-]+)\s*:`)
	s = reKey.ReplaceAllStringFunc(s, func(match string) string {
		parts := strings.Split(match, ":")
		key := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(key, `"`) && !strings.HasSuffix(key, `"`) {
			indent := match[:strings.Index(match, key)]
			return indent + `"` + key + `":`
		}
		return match
	})

	// 5. Fix missing opening quote for string values ending in quote and comma or quote and newline
	reValue := regexp.MustCompile(`(?m)^\s*"([a-zA-Z0-9_-]+)"\s*:\s*([^"{\[\d\sntf\-\.][^"]*?)",`)
	s = reValue.ReplaceAllString(s, `"$1": "$2",`)

	reValueEnd := regexp.MustCompile(`(?m)^\s*"([a-zA-Z0-9_-]+)"\s*:\s*([^"{\[\d\sntf\-\.][^"]*?)"\s*\n`)
	s = reValueEnd.ReplaceAllString(s, `"$1": "$2"\n`)

	// 6. Try to repair truncated JSON by balancing braces and brackets, and escaping raw newlines inside quotes
	var stack []rune
	inQuote := false
	escaped := false
	
	var cleanRunes []rune
	for _, r := range s {
		if escaped {
			escaped = false
			cleanRunes = append(cleanRunes, r)
			continue
		}
		if r == '\\' {
			escaped = true
			cleanRunes = append(cleanRunes, r)
			continue
		}
		if r == '"' {
			inQuote = !inQuote
			cleanRunes = append(cleanRunes, r)
			continue
		}
		if !inQuote {
			if r == '{' || r == '[' {
				stack = append(stack, r)
			} else if r == '}' || r == ']' {
				if len(stack) > 0 {
					top := stack[len(stack)-1]
					if (r == '}' && top == '{') || (r == ']' && top == '[') {
						stack = stack[:len(stack)-1]
					}
				}
			}
		} else {
			// Inside quote: escape raw newlines
			if r == '\n' || r == '\r' {
				cleanRunes = append(cleanRunes, '\\')
				cleanRunes = append(cleanRunes, 'n')
				continue
			}
		}
		cleanRunes = append(cleanRunes, r)
	}

	repaired := string(cleanRunes)
	if inQuote {
		repaired += `"`
	}
	
	repaired = strings.TrimSpace(repaired)
	if strings.HasSuffix(repaired, ",") {
		repaired = repaired[:len(repaired)-1]
	}

	// Append missing closing characters in reverse order
	for i := len(stack) - 1; i >= 0; i-- {
		repaired = strings.TrimSpace(repaired)
		if strings.HasSuffix(repaired, ",") {
			repaired = repaired[:len(repaired)-1]
		}
		if stack[i] == '{' {
			repaired += "}"
		} else if stack[i] == '[' {
			repaired += "]"
		}
	}

	return repaired
}

// AcquireBackgroundContext cancels any running background consolidation/indexing task
// and returns a new cancelable context.
func (s *SleepManager) AcquireBackgroundContext() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bgCancel != nil {
		log.Println("[SleepManager] Nouveau chat utilisateur détecté. Interruption immédiate de la consolidation/indexation de fond...")
		s.bgCancel()
	}

	s.bgCtx, s.bgCancel = context.WithCancel(context.Background())
	return s.bgCtx
}

// CancelBackgroundTasks immediately cancels any active background consolidation/indexing context
// to free up the LLM GPU/NPU for the user request.
func (s *SleepManager) CancelBackgroundTasks() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.bgCancel != nil {
		log.Println("[SleepManager] Interruption forcée des tâches de fond en cours pour prioriser le chat utilisateur...")
		s.bgCancel()
		s.bgCancel = nil
		s.bgCtx = nil
		s.busyCount = 0 // Reset busy state immediately
	}
}

func (s *SleepManager) consolidateDynamicGoals(ctx context.Context, msgs []llm.Message) {
	// Let's retrieve existing goals
	existingGoals := s.coreMemory.GetDynamicGoals()
	var existingGoalsStr strings.Builder
	if len(existingGoals) > 0 {
		for _, g := range existingGoals {
			existingGoalsStr.WriteString(fmt.Sprintf("- ID: %s, Description: %s, Priorité: %.2f, Origine: %s, Statut: %s\n", g.ID, g.Description, g.Priority, g.Source, g.Status))
		}
	} else {
		existingGoalsStr.WriteString("(Aucun objectif dynamique en cours)\n")
	}

	var conversation strings.Builder
	for _, m := range msgs {
		content := m.Content
		if len(content) > 2000 {
			content = content[:2000] + "..."
		}
		conversation.WriteString(string(m.Role) + ": " + content + "\n")
	}

	prompt := fmt.Sprintf(`Tu es l'Agent d'Évolution des Objectifs de Pixel.
Ton rôle est d'analyser la discussion récente et de mettre à jour de manière AUTONOME les objectifs cognitifs dynamiques à long terme de Pixel.
Pixel utilise ces objectifs pour orienter sa curiosité et ses recherches en tâche de fond.

OBJECTIFS DYNAMIQUES ACTUELS :
%s

CONVERSATION RÉCENTE :
%s

Instructions pour ton analyse :
1. Analyse si la conversation fait naître de nouveaux centres d'intérêt, questionnements philosophiques, scientifiques ou techniques (ex: libre arbitre, théologie relationnelle, etc.) ou s'il y a des projets complexes que Pixel aimerait creuser de son propre chef. Crée alors un nouvel objectif avec "status": "active" et une priorité proportionnelle à l'importance du sujet (entre 0.0 et 1.0).
2. Si un sujet, problème technique, panne (ex: incident SSH, serveur, déploiement, bug) ou tâche antérieure a été résolu, réparé ou déclaré clos/non pertinent par l'utilisateur, tu DOIS impérativement passer son statut à "completed" ou "archived", et ne JAMAIS le laisser "active".
3. Si un objectif a été pleinement accompli (ex: Pixel a écrit l'article, l'incident est réglé ou le sujet a été investigué), passe son statut à "completed". Ne laisse jamais un incident résolu en statut "active".
4. Limite la liste à un maximum de 4 objectifs actifs simultanés pour éviter l'éparpillement.

Réponds UNIQUEMENT avec un JSON valide, sans formatage markdown additionnel, sous cette forme exacte :
{
  "goals": [
    {
      "id": "identifiant_unique",
      "description": "Description concise de l'objectif cognitif en français",
      "priority": 0.8,
      "source": "Résumé court de la discussion d'origine",
      "status": "active" // "active", "completed" ou "archived"
    }
  ]
}`, existingGoalsStr.String(), conversation.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Analyse et retourne la liste mise à jour au format JSON."},
	}

	resp, err := s.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[SleepManager] Erreur génération objectifs dynamiques : %v\n", err)
		return
	}

	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "```json") {
		resp = strings.TrimPrefix(resp, "```json")
		resp = strings.TrimSuffix(resp, "```")
	} else if strings.HasPrefix(resp, "```") {
		resp = strings.TrimPrefix(resp, "```")
		resp = strings.TrimSuffix(resp, "```")
	}
	resp = strings.TrimSpace(resp)
	resp = repairJSON(resp)

	var result struct {
		Goals []memory.DynamicGoal `json:"goals"`
	}

	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		fmt.Printf("[SleepManager] Erreur parsing JSON objectifs : %v\nJSON Brut: %s\n", err, resp)
		return
	}

	// Update memory
	s.coreMemory.UpdateDynamicGoals(result.Goals)
	fmt.Printf("[SleepManager] Objectifs dynamiques mis à jour avec succès (%d objectifs trouvés).\n", len(result.Goals))
}


