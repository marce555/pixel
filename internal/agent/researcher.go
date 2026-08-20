package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/scheduler"
)

type ResearchAction struct {
	Type   string `json:"type"`   // "search", "scrape", "finish"
	Target string `json:"target"` // query for search, url for scrape, synthesis text for finish
}

type WebResearcherAgent struct {
	llmProvider llm.Provider
	webAgent    *WebAgent
}

func NewWebResearcherAgent(provider llm.Provider, webAgent *WebAgent) *WebResearcherAgent {
	return &WebResearcherAgent{
		llmProvider: provider,
		webAgent:    webAgent,
	}
}

func (r *WebResearcherAgent) RunResearch(ctx context.Context, query string) string {
	systemPrompt := fmt.Sprintf(`Tu es le Pilote de l'Agent de Recherche Web. 
Ta mission est de chercher l'information pour répondre à la requête de l'utilisateur.
Information temporelle : La date actuelle est le %s.

RÈGLES D'OR : 
- Ne te contente pas des simples résumés de recherche.
- Tu DOIS visiter les pages web complexes ("scrape" ou "browse") à partir des liens trouvés.
- Il te faut au minimum 2 ou 3 sources différentes avant de terminer la tâche.
- Les informations que tu explores sont AUTOMATIQUEMENT lues, extraites et sauvegardées dans un bloc-notes de fond. Tu n'as pas besoin de retenir les textes.
- Ton rôle est UNIQUEMENT de choisir la prochaine destination (prochain lien ou nouvelle recherche).

Actions possibles :
1. "search" : chercher sur Google. Fournis les mots-clés dans "target".
2. "news" : actualités. Fournis les mots-clés dans "target".
3. "scrape" : lire une URL classique. Fournis l'URL dans "target".
4. "browse" : lire une URL dynamique (JavaScript). Fournis l'URL dans "target".
5. "consult_gemini" : consulter l'IA cloud Gemini pour approfondir un sujet, obtenir une analyse philosophique ou scientifique poussée, ou vérifier/clarifier une information complexe. Fournis la question ou le sujet précis à approfondir dans "target". (IMPORTANT : Reste vigilant face aux affirmations de Gemini, croise et vérifie ses théories si nécessaire).
6. "finish" : QUAND tu as visité assez de pages et accumulé au moins 2 sources dans le bloc-notes. Laisse "target" vide.

Réponds UNIQUEMENT en JSON :
{
	"type": "action_type",
	"target": "your_target_here"
}
`, time.Now().Format("02/01/2006 à 15:04"))

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("Requête de recherche : %s", query)},
	}

	scratchpad := ""
	sourcesCount := 0
	maxSteps := 15
	for step := 1; step <= maxSteps; step++ {
		fmt.Printf("[Researcher] Étape %d/%d...\n", step, maxSteps)
		
		resp, err := r.llmProvider.Generate(ctx, messages)
		if err != nil {
			return fmt.Sprintf("Erreur lors de la réflexion de recherche : %v", err)
		}
		
		// Clean JSON
		respClean := strings.TrimSpace(resp)
		if strings.HasPrefix(respClean, "```json") {
			respClean = strings.TrimPrefix(respClean, "```json")
			respClean = strings.TrimSuffix(respClean, "```")
		} else if strings.HasPrefix(respClean, "```") {
			respClean = strings.TrimPrefix(respClean, "```")
			respClean = strings.TrimSuffix(respClean, "```")
		}
		respClean = strings.TrimSpace(respClean)
		
		// Tentative d'extraction de la partie JSON (si le LLM a bavardé avant ou après)
		jsonStart := strings.Index(respClean, "{")
		jsonEnd := strings.LastIndex(respClean, "}")
		if jsonStart != -1 && jsonEnd != -1 && jsonEnd > jsonStart {
			respClean = respClean[jsonStart : jsonEnd+1]
		}

		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: respClean})

		var action ResearchAction
		err = json.Unmarshal([]byte(repairJSON(respClean)), &action)
		if err != nil {
			// Try to recover or finish if format fails
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "Erreur de format JSON. Ne fais aucun commentaire. Réponds UNIQUEMENT en JSON valide commençant par '{' avec 'type' et 'target'."})
			continue
		}

		if action.Type == "finish" {
			if scratchpad == "" {
				return "La recherche n'a pas permis de trouver d'informations pertinentes."
			}
			return r.generateFinalReport(ctx, query, scratchpad)
		}

		var observation string
		if action.Type == "search" {
			fmt.Printf("[Researcher] Recherche web : %s\n", action.Target)
			searchRes, err := r.webAgent.SearchWeb(action.Target)
			if err != nil { observation = fmt.Sprintf("Erreur : %v", err) } else { observation = searchRes }
		} else if action.Type == "news" {
			fmt.Printf("[Researcher] Recherche d'actualités : %s\n", action.Target)
			newsRes, err := r.webAgent.SearchNews(action.Target)
			if err != nil { observation = fmt.Sprintf("Erreur : %v", err) } else { observation = newsRes }
		} else if action.Type == "scrape" {
			fmt.Printf("[Researcher] Scraping URL : %s\n", action.Target)
			scrapeRes, err := r.webAgent.ReadURLContent(action.Target)
			if err != nil { observation = fmt.Sprintf("Erreur : %v", err) } else { observation = scrapeRes }
		} else if action.Type == "browse" {
			fmt.Printf("[Researcher] Navigation dynamique URL : %s\n", action.Target)
			browseRes, err := r.webAgent.ReadURLDynamic(action.Target)
			if err != nil { observation = fmt.Sprintf("Erreur : %v", err) } else { observation = browseRes }
		} else if action.Type == "consult_gemini" {
			fmt.Printf("[Researcher] Consultation de Gemini : %s\n", action.Target)
			type cloudProviderGetter interface {
				GetCloudProvider() llm.Provider
			}
			var geminiProvider llm.Provider
			if getter, ok := r.llmProvider.(cloudProviderGetter); ok {
				geminiProvider = getter.GetCloudProvider()
			}
			if geminiProvider == nil {
				observation = "Erreur : L'accès à l'API Gemini n'est pas disponible dans le fournisseur actuel."
			} else {
				geminiMsg := []llm.Message{
					{Role: llm.RoleSystem, Content: "Tu es l'IA cloud Gemini. Tu es consultée par l'agent autonome Pixel pour approfondir un sujet scientifique ou philosophique ou pour vérifier une information. Réponds avec précision, rigueur et concision."},
					{Role: llm.RoleUser, Content: action.Target},
				}
				geminiRes, err := geminiProvider.Generate(ctx, geminiMsg)
				if err != nil {
					observation = fmt.Sprintf("Erreur lors de la consultation de Gemini : %v", err)
				} else {
					observation = fmt.Sprintf("[RÉPONSE DE GEMINI] :\n%s\n\n(Note pour Pixel : Reste critique, vigilant et vérifie ce qui est affirmé s'il s'agit de théories complexes ou de faits non vérifiés.)", geminiRes)
				}
			}
		} else {
			observation = "Action inconnue."
		}

		// Truncate observation to avoid overloading the local LLM context (e.g. OOM or context limit crash)
		if len(observation) > 8000 {
			observation = observation[:8000] + "\n...[CONTENU TRONQUÉ CAR TROP LONG]..."
		}

		// EXTRACTION VERS LE SCRATCHPAD
		extractPrompt := fmt.Sprintf(`La requête de l'utilisateur est : "%s"

Voici le contenu brut trouvé suite à l'action "%s" sur "%s" :
%s

Consignes :
Extrais UNIQUEMENT les informations (faits, chiffres, explications) qui répondent ou sont utiles à la requête initiale.
- Si le contenu est un menu, des publicités ou n'a aucun rapport direct avec la requête, réponds EXACTEMENT "Rien de pertinent."
- Sinon, résume l'information utile sous forme de notes claires.`, query, action.Type, action.Target, observation)

		extractMsg := []llm.Message{
			{Role: llm.RoleSystem, Content: fmt.Sprintf("Tu es un assistant strict d'extraction de données. Pas de salutations. La date actuelle est le %s.", time.Now().Format("02/01/2006 à 15:04"))},
			{Role: llm.RoleUser, Content: extractPrompt},
		}

		extracted, extErr := r.llmProvider.Generate(ctx, extractMsg)
		var shortSummary string
		if extErr == nil {
			extracted = strings.TrimSpace(extracted)
			if !strings.Contains(strings.ToLower(extracted), "rien de pertinent") && len(extracted) > 10 {
				scratchpad += fmt.Sprintf("### Source : %s\n%s\n\n", action.Target, extracted)
				sourcesCount++
				snippet := extracted
				if len(snippet) > 150 {
					snippet = snippet[:150] + "..."
				}
				shortSummary = fmt.Sprintf("Succès. Le bloc-notes contient maintenant %d source(s) utile(s). Aperçu de ce qui a été sauvegardé : %s", sourcesCount, snippet)
				fmt.Println("[Researcher] -> Données extraites ajoutées au scratchpad.")
			} else {
				shortSummary = "Aucune information pertinente n'a été trouvée dans cette source."
				fmt.Println("[Researcher] -> Source non pertinente ignorée.")
			}
		} else {
			shortSummary = fmt.Sprintf("Erreur d'extraction : %v", extErr)
		}

		messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Observation : %s", shortSummary)})
	}

	// Épuisement des étapes : on retourne le scratchpad formaté
	if scratchpad == "" {
		return "La recherche a épuisé ses étapes sans rien trouver de pertinent."
	}
	return r.generateFinalReport(ctx, query, scratchpad)
}

// generateFinalReport takes the accumulated scratchpad notes and writes a beautifully formatted Markdown report.
func (r *WebResearcherAgent) generateFinalReport(ctx context.Context, query string, scratchpad string) string {
	fmt.Println("[Researcher] Rédaction du rapport final...")
	
	systemPrompt := fmt.Sprintf("Tu es un Analyste de Données Expert. Ta mission est de rédiger un rapport final détaillé et structuré en réponse à la requête de l'utilisateur.\nInformation importante : La date actuelle est le %s.", time.Now().Format("02/01/2006 à 15:04"))
	userPrompt := fmt.Sprintf(`Requête initiale : "%s"

Voici les informations brutes récoltées pendant la recherche :
%s

Consignes de rédaction :
1. Rédige un rapport complet, fluide et très structuré en utilisant Markdown.
2. Formule de vraies phrases, ne te contente pas de recracher des puces. Explique les faits de manière professionnelle.
3. Utilise des sous-titres (##) si la réponse aborde plusieurs thèmes.
4. Ne parle pas de "bloc-notes", "brouillon" ou "scratchpad". Présente les informations de façon formelle et directe.
5. Termine OBLIGATOIREMENT ton rapport par une section "### Sources" qui liste les liens/URLs présents dans les notes.
6. Ne fais pas d'introduction du type "Voici le rapport...". Commence directement.`, query, scratchpad)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	report, err := r.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[Researcher] Erreur lors de la génération du rapport final : %v\n", err)
		return strings.TrimSpace(scratchpad) // Fallback: return raw notes if LLM fails
	}

	return strings.TrimSpace(report)
}

// NewResearchTaskHandler creates a scheduler.TaskHandler for asynchronous deep research.
func NewResearchTaskHandler(researcher *WebResearcherAgent, broadcaster EventBroadcaster, stm *memory.STM) scheduler.TaskHandler {
	return func(ctx context.Context, task *scheduler.Task) error {
		task.AppendLog(fmt.Sprintf("Démarrage de la recherche profonde : '%s'", task.Payload))
		
		synthesis := researcher.RunResearch(ctx, task.Payload)
		
		task.AppendLog("Recherche terminée. Diffusion du rapport...")

		formattedResponse := fmt.Sprintf(`🤖 **[Recherche Web Terminée]**
*J'ai terminé ma recherche approfondie sur : "%s"*

---

%s`, task.Payload, synthesis)

		broadcaster.Broadcast(formattedResponse)
		stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: formattedResponse})
		task.AppendLog("Rapport diffusé au chat utilisateur avec succès !")
		
		return nil
	}
}
