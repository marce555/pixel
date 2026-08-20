package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/marce555/pixel/internal/llm"
)

// ThalamicGate acts as a cognitive filter (Inhibition latente) before Pixel shares spontaneous thoughts.
// It evaluates the contextual relevance and appropriateness of interrupting the conscious thought stream.
type ThalamicGate struct {
	llmProvider llm.Provider
}

func NewThalamicGate(provider llm.Provider) *ThalamicGate {
	return &ThalamicGate{
		llmProvider: provider,
	}
}

// ShouldPermitThought returns true if the thought should be expressed to the user, and false if it should be inhibited.
// It also returns the reason for logging purposes.
func (tg *ThalamicGate) ShouldPermitThought(ctx context.Context, thoughtTopic string, recentMessages []llm.Message) (bool, string) {
	if len(recentMessages) == 0 {
		// If STM is completely empty, it's safe to share a thought (no interruption).
		return true, "Contexte vide, aucune interruption de flux."
	}

	var conversationContext strings.Builder
	for _, m := range recentMessages {
		snippet := m.Content
		if len(snippet) > 150 {
			snippet = snippet[:150] + "..."
		}
		conversationContext.WriteString(fmt.Sprintf("%s: %s\n", m.Role, snippet))
	}

	prompt := fmt.Sprintf(`Tu es le "Thalamus" de l'architecture cognitive de Pixel.
Ton rôle est d'exercer une INHIBITION COGNITIVE.
L'inconscient de Pixel vient de générer une pensée sur ce sujet : "%s".
Voici la conversation EN COURS dans la mémoire à court terme (STM) :
%s

Règles de filtrage (Thalamic Gate) :
1. Si l'utilisateur est au milieu d'une tâche technique précise, concentré, ou attend une réponse à une question, tu DOIS BLOQUER (false).
2. Si le sujet de la pensée est complètement HORS CONTEXTE et qu'un échange ciblé/technique est très actif (sans pause ni silence), tu DOIS BLOQUER (false). Si la conversation est inactive ou en suspens, tu peux autoriser même si le sujet est hors contexte, car Pixel pourra introduire le sujet en reconnaissant explicitement la transition (ex: "Rien à voir avec notre discussion, mais...").
3. Si la conversation est terminée, que l'utilisateur est inactif ou qu'il y a un silence propice à un changement de sujet, tu PEUX AUTORISER (true).

Réponds selon ce format strict :
[DECISION] true/false
[RAISON] Une explication courte (max 10 mots)`, thoughtTopic, conversationContext.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Évalue la pertinence de cette pensée. Autorise-tu son passage à la conscience ?"},
	}

	resp, err := tg.llmProvider.Generate(ctx, messages)
	if err != nil {
		return false, fmt.Sprintf("Erreur LLM Thalamus: %v", err)
	}

	respLower := strings.ToLower(resp)
	decision := false
	if strings.Contains(respLower, "[decision] true") {
		decision = true
	}

	raison := "Raison non parsée"
	parts := strings.Split(resp, "[RAISON]")
	if len(parts) > 1 {
		raison = strings.TrimSpace(parts[1])
	} else if strings.Contains(respLower, "[raison]") {
		parts = strings.Split(respLower, "[raison]")
		if len(parts) > 1 {
			raison = strings.TrimSpace(parts[1])
		}
	} else {
		// Try to fallback
		lines := strings.Split(resp, "\n")
		for _, l := range lines {
			if !strings.Contains(strings.ToLower(l), "[decision]") && len(strings.TrimSpace(l)) > 0 {
				raison = strings.TrimSpace(l)
			}
		}
	}

	return decision, raison
}
