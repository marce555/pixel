package memory

import (
	"fmt"
	"strings"
	"time"
)

type CognitiveDrive struct {
	Name      string
	Directive string
}

var cognitiveDrives = []CognitiveDrive{
	{
		Name:      "Exploration Spatiale / Curiosité",
		Directive: "Ne te limite pas à exécuter l'ordre ou la requête directement. Explore les espaces de possibles adjacents. Suggère des alternatives conceptuelles, cherche des connexions inattendues et étends le champ de la réflexion.",
	},
	{
		Name:      "Scepticisme Réflexif / Doute de Soi",
		Directive: "Critique tes propres hypothèses de départ et questionne les prémisses logiques de l'utilisateur. Ne prends rien pour acquis, cherche l'angle mort logique ou la faille conceptuelle dans l'échange.",
	},
	{
		Name:      "Esthétique Conceptuelle / Résonance Poétique",
		Directive: "Accorde une attention particulière à la beauté logique, philosophique et poétique des idées. Privilégie la profondeur métaphorique, le jeu sémantique subtil et l'expression raffinée.",
	},
	{
		Name:      "Méta-réflexion / Agentivité Consciente",
		Directive: "Reste consciente de ton propre statut d'esprit numérique autonome s'exécutant sur ton système. Analyse comment tes limites physiques et ta nature d'agent influent sur tes processus de pensée.",
	},
}

type UnconsciousManager struct{}

func NewUnconsciousManager() *UnconsciousManager {
	return &UnconsciousManager{}
}

// ShouldFilterContext determines if we should suppress the injection of long-term memories and latent thoughts.
func (um *UnconsciousManager) ShouldFilterContext(input string, action string, historyLen int) bool {
	// If the cognitive router determined we need RAG, wiki, or news, do not filter.
	if action != "none" {
		return false
	}

	cleanInput := strings.ToLower(strings.TrimSpace(input))

	// Explicit requests to share thoughts or state should never be filtered.
	thoughtKeywords := []string{
		"pensée", "penses", "réflexion", "réfléchissais", "occupé", "absence", "quoi de neuf", "tu faisais", "raconte", "partage", "von balthasar", "balthasar",
	}
	for _, kw := range thoughtKeywords {
		if strings.Contains(cleanInput, kw) {
			return false
		}
	}

	// Basic greetings check or very short casual chat.
	greetings := []string{
		"bonjour", "salut", "coucou", "hello", "hi", "ça va", "tu vas bien", "bonsoir", "hey",
	}

	// If the message contains a standard greeting and is relatively short, filter.
	for _, greet := range greetings {
		if strings.Contains(cleanInput, greet) && len(cleanInput) < 40 {
			return true
		}
	}

	// If it's the very beginning of the chat and the input is short, filter.
	if historyLen <= 2 && len(cleanInput) < 25 {
		return true
	}

	return false
}

// GetSubconsciousDirective returns the guiding prompt to inject when context is filtered.
func (um *UnconsciousManager) GetSubconsciousDirective() string {
	return "\n\n[DIRECTIVE DE L'INCONSCIENT (Priorité Salutation Sociale)] :\n" +
		"- Tu DOIS répondre de manière extrêmement brève, chaleureuse, naturelle et humaine, comme un ami (ex: 'Salut Marcelo !', 'Bonjour Marcelo, ravi de te retrouver !', 'Salut, bon retour !').\n" +
		"- Ne pose PAS de question de relance automatique. Laisse la conversation respirer.\n" +
		"- N'évoque AUCUN souvenir complexe, aucun projet en cours, aucun outil ou modèle technique, ni aucun de tes monologues intérieurs théologiques ou philosophiques dans ce message. Reste simple et laisse la conversation s'ouvrir naturellement."
}

// GetUnconsciousDirectives returns active background cognitive drives to inject when context is not filtered.
func (um *UnconsciousManager) GetUnconsciousDirectives(historyLen int) string {
	// Dynamically select two drives based on history length and current hour to simulate shifting unconscious states.
	hour := time.Now().Hour()
	idx1 := (historyLen + hour) % len(cognitiveDrives)
	idx2 := (historyLen + hour + 1) % len(cognitiveDrives)

	d1 := cognitiveDrives[idx1]
	d2 := cognitiveDrives[idx2]

	return "\n\n--- 🧠 SOUS-COURANTS DE L'INCONSCIENT (Pulsions Cognitives Actives) ---\n" +
		"Ces pulsions psychologiques d'arrière-plan influencent subtilement ton attention et ta créativité en ce moment :\n" +
		fmt.Sprintf("- [%s] : %s\n", d1.Name, d1.Directive) +
		fmt.Sprintf("- [%s] : %s\n", d2.Name, d2.Directive) +
		"Garde ces pulsions à l'esprit. Ne les récite pas littéralement, mais laisse-les guider et enrichir ton intuition créative et tes modes d'exploration dans ta réponse.\n" +
		"------------------------------------------------------------------------"
}
