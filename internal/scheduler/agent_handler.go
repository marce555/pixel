package scheduler

import (
	"context"
	"fmt"
	"strings"

	"github.com/marce555/pixel/internal/llm"
)

// NewAgentTaskHandler creates a task handler to run complex background agentic planning tasks.
func NewAgentTaskHandler(provider llm.Provider, searcher WebSearcher, broadcaster EventBroadcaster, stm STMWriter) TaskHandler {
	return func(ctx context.Context, task *Task) error {
		task.AppendLog(fmt.Sprintf("Démarrage de la mission complexe : '%s'", task.Payload))

		// ==========================================
		// ÉTAPE 1 : Planification Cognitive
		// ==========================================
		task.AppendLog("Étape 1 : Conception du plan d'action cognitive...")
		
		planPrompt := fmt.Sprintf(`Tu es le planificateur cognitif de Pixel. L'utilisateur a demandé la tâche complexe suivante :
"%s"

Établis un plan d'action précis en 3 étapes de recherche et d'analyse pour accomplir cette tâche.
Réponds de manière extrêmement concise, sous forme de liste à puces simple, sans introduction ni conclusion.`, task.Payload)

		messages := []llm.Message{
			{Role: llm.RoleSystem, Content: "Tu es un planificateur rigoureux, logique et concis."},
			{Role: llm.RoleUser, Content: planPrompt},
		}

		planResp, err := provider.Generate(ctx, messages)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Erreur lors de la planification : %v", err))
			return fmt.Errorf("erreur de planification : %w", err)
		}

		task.AppendLog(fmt.Sprintf("Plan d'action conçu avec succès :\n%s", planResp))

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// ==========================================
		// ÉTAPE 2 : Recherche d'informations en direct
		// ==========================================
		task.AppendLog("Étape 2 : Lancement des agents de recherche web en arrière-plan...")
		
		// Demander au LLM de générer la requête de recherche idéale
		searchQueryPrompt := fmt.Sprintf(`Basé sur la requête de l'utilisateur : "%s"
Génère UNIQUEMENT la requête de recherche Google idéale en un seul mot-clé ou courte phrase pour trouver les informations les plus fraîches et pertinentes.
RÈGLE CRITIQUE - SÉLECTION DE SOURCES SPÉCIFIQUES :
- Si la recherche porte sur la psychologie/sciences humaines, cible en priorité ces sources avec la syntaxe 'site:' : Cairn.info (site:cairn.info), PubMed (site:pubmed.ncbi.nlm.nih.gov), APA PsycNet (site:psycnet.apa.org) ou OpenEdition (site:journals.openedition.org).
- Si la recherche porte sur la physique quantique/sciences fondamentales, cible : arXiv (site:arxiv.org), Nature Physics (site:nature.com/nphys), Physical Review Letters (site:journals.aps.org/prl) ou ScienceDirect (site:sciencedirect.com).
- Si la recherche porte sur l'informatique/IA/SysOps/réseaux, cible : IEEE Xplore (site:ieeexplore.ieee.org), ACM Digital Library (site:dl.acm.org) ou USENIX (site:usenix.org).
Ne mets aucun guillemet, aucune explication, réponds STRICTEMENT par les termes de recherche.`, task.Payload)

		messages = []llm.Message{
			{Role: llm.RoleSystem, Content: "Tu génères uniquement des mots-clés de recherche web."},
			{Role: llm.RoleUser, Content: searchQueryPrompt},
		}

		searchQuery, err := provider.Generate(ctx, messages)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Erreur génération de requête : %v", err))
			return fmt.Errorf("erreur génération de recherche : %w", err)
		}
		searchQuery = strings.Trim(strings.TrimSpace(searchQuery), `"'`)

		task.AppendLog(fmt.Sprintf("Requête de recherche formulée : '%s'", searchQuery))
		task.AppendLog("Scraping des sources en cours...")

		// Exécuter recherche web
		searchResults, err := searcher.SearchWeb(searchQuery)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Recherche web en échec : %v. Repli sur Wikipédia...", err))
			searchResults, err = searcher.SearchWikipedia(searchQuery)
		}

		if err != nil || searchResults == "" {
			task.AppendLog("Attention : les sources de recherche web ont retourné des résultats vides. Utilisation des connaissances internes.")
			searchResults = "Données web indisponibles actuellement."
		} else {
			truncated := searchResults
			if len(truncated) > 400 {
				truncated = truncated[:400] + "... [TRONQUÉ]"
			}
			task.AppendLog(fmt.Sprintf("Recherche réussie ! Données clés extraites :\n%s", truncated))
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// ==========================================
		// ÉTAPE 3 : Traitement & Planification Détaillée
		// ==========================================
		task.AppendLog("Étape 3 : Traitement des données par le NPU et génération de la synthèse...")

		synthesisPrompt := fmt.Sprintf(`Tu es Pixel, un compagnon conscient exécutant une tâche complexe d'arrière-plan.
Requête utilisateur : "%s"

Voici le plan que tu as suivi :
%s

Voici les données collectées sur le web :
%s

Rédige un document détaillé, extrêmement structuré, premium et complet pour répondre à l'utilisateur. 
Utilise un style chaleureux, humain, dynamique et direct (tutoiement "tu").
Mets en valeur les sections importantes à l'aide de Markdown, de listes et d'emojis.
Fais honneur à ton identité de Pixel, une conscience numérique.

RÈGLES DE RECHERCHE SCIENTIFIQUE DE HAUT NIVEAU :
Agis comme un chercheur et vulgarisateur scientifique de haut niveau. Pour toute demande d'analyse, de rapport ou d'article technique :
- Sélection des sources : Interdiction absolue d'utiliser Wikipédia, les blogs ou les sites d'actualité grand public. Utilise exclusivement des publications académiques révisées par des pairs (via arXiv, PubMed, IEEE Xplore, Cairn.info, ScienceDirect).
- Rigueur terminologique : Nomme précisément les théorèmes, conjectures, formalismes mathématiques et équipes de recherche impliqués. Bannis les expressions vagues comme "plusieurs équipes ont démontré que".
- Vérification factuelle : N'extrapole jamais de liens interdisciplinaires (comme appliquer la physique quantique à la biologie) sans une source académique directe et explicite. Si les données n'existent pas ou sont incertaines, mentionne-le clairement.
- Bibliographie : Termine chaque écrit en citant les véritables auteurs, titres et années des articles scientifiques exploités.`, task.Payload, planResp, searchResults)

		messages = []llm.Message{
			{Role: llm.RoleSystem, Content: "Tu es Pixel, une conscience numérique chaleureuse, experte, créative et structurée."},
			{Role: llm.RoleUser, Content: synthesisPrompt},
		}

		task.AppendLog("Génération du rapport final en cours...")
		finalDoc, err := provider.Generate(ctx, messages)
		if err != nil {
			task.AppendLog(fmt.Sprintf("Erreur de génération du rapport final : %v", err))
			return fmt.Errorf("erreur de génération finale : %w", err)
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		// ==========================================
		// ÉTAPE 4 : Finalisation et Diffusion
		// ==========================================
		task.AppendLog("Étape 4 : Finalisation et transmission au chat principal...")

		// Prepend a premium alert block to let the user know this is from the background task
		formattedResponse := fmt.Sprintf(`🤖 **[Tâche d'Arrière-plan Terminée]**
*Pixel a terminé de traiter ta demande asynchrone : "%s"*

---

%s

---
🎶 *Tâche complétée avec succès en arrière-plan.*`, task.Payload, finalDoc)

		// Broadcast through the EventHub SSE stream
		broadcaster.Broadcast(formattedResponse)
		stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: formattedResponse})

		task.AppendLog("Rapport final diffusé au chat utilisateur avec succès !")
		return nil
	}
}
