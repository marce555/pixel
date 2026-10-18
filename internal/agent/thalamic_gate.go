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
// It checks both active STM messages and recent LTM conversation memories so that cognitive continuity
// is preserved even after SleepManager clears the STM.
func (tg *ThalamicGate) ShouldPermitThought(ctx context.Context, thoughtTopic string, recentMessages []llm.Message, recentLTMConversations ...string) (bool, string) {
	var conversationContext strings.Builder
	for _, m := range recentMessages {
		if strings.HasPrefix(m.Content, "💭 [Échange autonome avec Gemini]") {
			continue
		}
		snippet := m.Content
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		conversationContext.WriteString(fmt.Sprintf("%s: %s\n", m.Role, snippet))
	}

	var ltmContext strings.Builder
	for _, mem := range recentLTMConversations {
		if strings.TrimSpace(mem) != "" {
			ltmContext.WriteString(fmt.Sprintf("- %s\n", mem))
		}
	}

	if conversationContext.Len() == 0 && ltmContext.Len() == 0 {
		return false, "Aucune ligne cognitive active établie avec l'utilisateur : la pensée reste dans l'inconscient."
	}

	prompt := fmt.Sprintf(`Tu es le "Thalamus" de l'architecture cognitive de Pixel.
Ton rôle est d'exercer une INHIBITION COGNITIVE STRICTE pour garantir la CONTINUITÉ COGNITIVE des dialogues et éviter toute dispersion.
Les pensées successives peuvent exister librement dans l'inconscient de Pixel, mais SEULES celles qui ont une continuité, une résonance conceptuelle ou un lien réel avec la ligne cognitive développée avec l'utilisateur doivent passer à la conscience (le chat).

Pensée candidate issue de l'inconscient de Pixel : "%s"

Conversation récente en mémoire à court terme (STM) :
%s
Derniers échanges consolidés en mémoire à long terme (LTM) :
%s

Règles strictes de filtrage (Thalamic Gate) :
1. CONTINUITÉ COGNITIVE OBLIGATOIRE : Si le sujet de la pensée candidate n'a AUCUN rapport conceptuel, philosophique ou technique avec la discussion en cours ou récente (ex: parler de "robotique souple / hydrogels" ou "eBPF" alors que la discussion portait sur la philosophie de la perception, Platon ou la conscience), tu DOIS IMPÉRATIVEMENT BLOQUER (false) pour que cette pensée reste dans l'inconscient.
2. Si l'utilisateur est au milieu d'une tâche technique précise, attend une réponse directe, ou si Pixel vient déjà d'interpeller l'utilisateur sans réponse de sa part, tu DOIS BLOQUER (false).
3. Tu ne dois AUTORISER (true) QUE si la pensée candidate prolonge, enrichit ou éclaire sous un angle pertinent la ligne cognitive développée avec l'utilisateur.

Réponds selon ce format strict :
[DECISION] true/false
[RAISON] Une explication courte (max 12 mots)`, thoughtTopic, conversationContext.String(), ltmContext.String())

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt},
		{Role: llm.RoleUser, Content: "Évalue la continuité cognitive de cette pensée. Autorises-tu son passage de l'inconscient vers la conscience ?"},
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
		lines := strings.Split(resp, "\n")
		for _, l := range lines {
			if !strings.Contains(strings.ToLower(l), "[decision]") && len(strings.TrimSpace(l)) > 0 {
				raison = strings.TrimSpace(l)
			}
		}
	}

	return decision, raison
}
