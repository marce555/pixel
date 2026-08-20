package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
)

// ProfilingAgent is a background agent that reads the STM to extract user traits.
type ProfilingAgent struct {
	llmProvider llm.Provider
	coreMemory  *memory.CoreMemory
}

func NewProfilingAgent(provider llm.Provider, coreMemory *memory.CoreMemory) *ProfilingAgent {
	return &ProfilingAgent{
		llmProvider: provider,
		coreMemory:  coreMemory,
	}
}

// AnalyzeAndProfile is called to update the Core Memory based on recent conversation.
func (p *ProfilingAgent) AnalyzeAndProfile(ctx context.Context, stmMsgs []llm.Message) {
	if len(stmMsgs) == 0 {
		return
	}

	var conversation strings.Builder
	for _, m := range stmMsgs {
		conversation.WriteString(string(m.Role) + ": " + m.Content + "\n")
	}

	currentProfile := p.coreMemory.GetProfile().Static
	currentProfileStr := fmt.Sprintf("- Nom: %s\n- Rôle: %s\n- Tech Stack: %s", currentProfile.Name, currentProfile.Role, currentProfile.TechStack)

	systemPrompt := `Tu es le ProfilingAgent. Ton rôle est d'analyser une conversation et d'extraire l'identité et les faits marquants de l'utilisateur.
Fusionne les nouvelles informations avec le profil statique existant.
Retourne UNIQUEMENT du JSON valide avec ce format, sans commentaires ni balises markdown :
{
  "name": "Nom de l'utilisateur (ou 'Inconnu')",
  "role": "Son métier ou rôle (ou 'Utilisateur')",
  "tech_stack": "Technologies utilisées sous forme de chaîne de caractères simple séparée par des virgules (ex: 'Go, Linux, système' - RÈGLE STRICTE : ne retourne jamais de tableau/array JSON ici, uniquement du texte brut)"
}

PROFIL ACTUEL :
` + currentProfileStr

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: "Voici la dernière conversation à analyser :\n" + conversation.String()},
	}

	responseJSON, err := p.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[ProfilingAgent] Erreur d'analyse: %v\n", err)
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

	var newProfile memory.StaticProfile
	if err := json.Unmarshal([]byte(responseJSON), &newProfile); err != nil {
		fmt.Printf("[ProfilingAgent] Erreur parsing JSON: %v\n", err)
		return
	}

	// Update Core Memory if something changed
	if newProfile.Name != currentProfile.Name || newProfile.Role != currentProfile.Role || newProfile.TechStack != currentProfile.TechStack {
		p.coreMemory.UpdateStaticProfile(newProfile)
		fmt.Println("[ProfilingAgent] Profil statique utilisateur mis à jour dans la Core Memory.")
	}

	// Extract and store personal facts into Volatile memory (always visible in system prompt)
	p.extractPersonalFacts(ctx, conversation.String())
}

// extractPersonalFacts runs a dedicated LLM call to extract key personal facts
// (children, city, occupation, etc.) and saves them to CoreMemory.Volatile.
// These facts are always injected into every system prompt, making them
// instantly retrievable without depending on RAG or vector search.
func (p *ProfilingAgent) extractPersonalFacts(ctx context.Context, conversation string) {
	currentVolatile := p.coreMemory.GetProfile().Volatile

	// Build a summary of already-known facts to avoid overwriting good data with empty values
	var knownFacts strings.Builder
	for k, v := range currentVolatile {
		knownFacts.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
	}
	if knownFacts.Len() == 0 {
		knownFacts.WriteString("(aucun fait personnel encore enregistré)\n")
	}

	systemPrompt := `Tu es l'extracteur de faits personnels de Pixel. Analyse la conversation pour extraire les informations personnelles EXPLICITES mentionnées par l'utilisateur.

Retourne UNIQUEMENT du JSON valide avec UNIQUEMENT les clés pour lesquelles tu as une valeur certaine et explicite dans la conversation. N'invente rien.
Format :
{
  "Enfants": "prénoms et âges si mentionnés (ex: 'Mathieu (17 ans), Lisa (13 ans)')",
  "Ville": "ville ou lieu de résidence si mentionné",
  "Occupation": "métier ou activité principale si mentionné",
  "Partenaire": "prénom du conjoint/partenaire si mentionné",
  "Projet actuel": "nom du projet principal si mentionné"
}

RÈGLES STRICTES :
- N'inclus une clé QUE si la valeur est clairement mentionnée dans la conversation.
- Si une information n'est pas dans la conversation, n'inclus PAS la clé.
- Si la valeur est déjà connue et que la conversation n'apporte pas de nouvelle info, n'inclus pas non plus la clé.
- Retourne {} si aucune nouvelle information personnelle n'est présente.

FAITS PERSONNELS DÉJÀ CONNUS :
` + knownFacts.String()

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: "Conversation à analyser :\n" + conversation},
	}

	resp, err := p.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[ProfilingAgent] Erreur extraction faits personnels: %v\n", err)
		return
	}

	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, "```json") {
		resp = strings.TrimPrefix(resp, "```json")
		resp = strings.TrimSuffix(strings.TrimSpace(resp), "```")
	} else if strings.HasPrefix(resp, "```") {
		resp = strings.TrimPrefix(resp, "```")
		resp = strings.TrimSuffix(strings.TrimSpace(resp), "```")
	}
	resp = strings.TrimSpace(resp)

	var facts map[string]string
	if err := json.Unmarshal([]byte(resp), &facts); err != nil {
		fmt.Printf("[ProfilingAgent] Erreur parsing faits personnels JSON: %v\n", err)
		return
	}

	updated := 0
	for key, value := range facts {
		if strings.TrimSpace(value) == "" {
			continue
		}
		existing := currentVolatile[key]
		if existing != value {
			p.coreMemory.UpdateVolatileState(key, value)
			fmt.Printf("[ProfilingAgent] Fait personnel mis à jour → %s: %s\n", key, value)
			updated++
		}
	}
	if updated > 0 {
		fmt.Printf("[ProfilingAgent] %d fait(s) personnel(s) sauvegardé(s) dans la Core Memory Volatile.\n", updated)
	}
}

// CheckForPersonaUpdate analyzes a single user message to see if it contains instructions to modify the agent's behavior.
func (p *ProfilingAgent) CheckForPersonaUpdate(ctx context.Context, userMessage string) {
	currentPersona := p.coreMemory.GetAgentPersona()

	systemPrompt := `Tu es l'agent d'adaptation du persona. Ton rôle est d'analyser le message de l'utilisateur pour voir s'il te donne une instruction claire sur la façon dont tu dois te comporter, parler, ou ton rôle (ex: "comporte-toi comme un pirate", "ne sois plus familier", "tu es maintenant un expert en physique").
Si oui, mets à jour ton persona actuel en y intégrant cette instruction. Assure-toi de conserver les instructions fondamentales (mémoire persistante, identité de base Pixel).
Si le message de l'utilisateur ne contient AUCUNE instruction de ce type, retourne EXACTEMENT la chaîne vide ou "NONE".
Retourne UNIQUEMENT le nouveau persona ou "NONE", sans commentaires.

PERSONA ACTUEL :
` + currentPersona

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("ANALYSE CE MESSAGE :\n\"%s\"\n\nRAPPEL CRITIQUE : Si ce message est une simple phrase de salutation, de mise à jour, de question ou de bavardage sans directive explicite de changement de rôle/comportement pour toi, réponds STRICTEMENT par 'NONE'. Sinon, réponds avec le persona adapté.", userMessage)},
	}

	response, err := p.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[ProfilingAgent] Erreur lors de l'analyse du persona: %v\n", err)
		return
	}

	response = strings.TrimSpace(response)
	if response != "" && response != "NONE" && response != currentPersona {
		// Guardrail pour éviter d'effacer accidentellement l'identité de Pixel
		if len(response) > 100 && strings.Contains(strings.ToLower(response), "pixel") {
			p.coreMemory.UpdateAgentPersona(response)
			fmt.Println("[ProfilingAgent] Persona (Prompt Système) mis à jour dynamiquement dans la Core Memory.")
		} else {
			fmt.Printf("[ProfilingAgent] Guardrail déclenché: Nouveau persona rejeté car trop court ou sans 'pixel' (%d caractères): '%s'\n", len(response), response)
		}
	}
}
