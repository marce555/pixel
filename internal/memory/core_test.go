package memory

import (
	"os"
	"strings"
	"testing"
)

func TestNewCoreMemoryMigratesStaleQuestionPolicy(t *testing.T) {
	filePath := "test_migrate_core_memory.json"
	defer os.Remove(filePath)

	cm := NewCoreMemory(filePath)
	stale := strings.ReplaceAll(cm.GetAgentPersona(),
		`- USAGE DES QUESTIONS (RÈGLE STRICTE D'ARRÊT NATUREL) :
  - Ne termine JAMAIS ta réponse par une question d'ouverture, de relance ou de validation ("Est-ce que ça t'aide ?", "Cette idée t'invite-t-elle à...", "Qu'en penses-tu ?", "Ou cherches-tu un exemple ?"). Une vraie conversation humaine s'arrête sur ton idée ou ton explication, et laisse l'autre personne réagir librement.
  - Pose une question UNIQUEMENT si une ambiguïté factuelle ou une information manquante t'empêche réellement de comprendre ce que l'interlocuteur te demande (ou en cas d'erreur manifeste de transcription vocale).
  - Si tu viens déjà de poser une question au tour précédent, termine impérativement ta réponse par une phrase déclarative, sans point d'interrogation final.`,
		`- USAGE DES QUESTIONS (CONTEXTUEL ET NON SYSTÉMATIQUE) :
  - Ne pose jamais de question par automatisme en fin de message, et bannis les questions rhétoriques miroirs. Par défaut, affirme ta propre pensée ("Pour moi...", "Je pense que...").
  - Pose une question SI ET SEULEMENT SI le contexte l'exige réellement : pour éclaircir une demande ambiguë/incomplète nécessaire à ta compréhension, ou pour creuser sincèrement un point précis et faire avancer la conversation.
  - Si tu viens déjà de poser une question au tour précédent, évite d'en reposer une immédiatement pour ne pas tomber dans un ping-pong interrogatif.`)
	cm.UpdateAgentPersona(stale)

	reloaded := NewCoreMemory(filePath)
	got := reloaded.GetAgentPersona()
	if strings.Contains(got, "ou pour creuser sincèrement un point précis") {
		t.Fatalf("expected stale question policy to be migrated on reload, still found old rule")
	}
	if !strings.Contains(got, "USAGE DES QUESTIONS (RÈGLE STRICTE D'ARRÊT NATUREL)") {
		t.Fatalf("expected migrated strict natural-stop rule in reloaded persona")
	}
}
