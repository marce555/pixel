package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/marce555/pixel/internal/llm"
	"github.com/marce555/pixel/internal/memory"
	"github.com/marce555/pixel/internal/scheduler"
)

type SysadminAction struct {
	Type    string `json:"type"`    // "ssh_cmd", "finish"
	Target  string `json:"target"`  // server user@ip
	Command string `json:"command"` // the bash command
}

type SysadminAgent struct {
	llmProvider llm.Provider
}

func NewSysadminAgent(provider llm.Provider) *SysadminAgent {
	return &SysadminAgent{
		llmProvider: provider,
	}
}

func (s *SysadminAgent) executeSSH(ctx context.Context, target string, command string) string {
	// Add protection against dangerous commands
	cmdLower := strings.ToLower(command)
	cmdWithoutDevNull := strings.ReplaceAll(cmdLower, "2>/dev/null", "")
	if strings.Contains(cmdLower, "rm -rf") || strings.Contains(cmdWithoutDevNull, ">") || strings.Contains(cmdLower, "reboot") || strings.Contains(cmdLower, "shutdown") || strings.Contains(cmdLower, "mkfs") {
		return "ACTION REFUSÉE : L'agent a reçu l'ordre d'être strictement en lecture seule. Les commandes destructrices ou modificatrices sont interdites."
	}
	// Run via ssh
	// Format: ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new target command
	sshCmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", target, command)
	out, err := sshCmd.CombinedOutput()
	output := string(out)
	if err != nil {
		output = fmt.Sprintf("Erreur SSH: %v\nOutput: %s", err, output)
	}
	
	// Truncate to avoid exploding context
	if len(output) > 5000 {
		output = output[:5000] + "\n...[TRONQUÉ]..."
	}
	if output == "" {
		output = "[Commande exécutée avec succès mais sans sortie]"
	}
	return output
}

func (s *SysadminAgent) RunDiagnosis(ctx context.Context, server string, problem string) string {
	systemPrompt := fmt.Sprintf(`Tu es le Pilote de l'Agent Administrateur Système (SysadminAgent).
Ta mission est d'enquêter sur le serveur "%s" pour diagnostiquer le problème suivant : "%s"

RÈGLES D'OR :
- Tu procèdes étape par étape (ReAct : Raisonnement puis Action).
- À chaque étape, tu peux lancer UNE commande SSH sur le serveur cible.
- Commence par des commandes globales (ex: systemctl status, journalctl, free, df) avant de lire des fichiers spécifiques (cat, tail).
- Tu as interdiction formelle d'utiliser des commandes destructrices (rm, reboot, etc). Uniquement de l'investigation en LECTURE SEULE.
- Les résultats des commandes sont stockés dans ton contexte.
- Quand tu as compris l'origine de la panne, choisis l'action "finish".

Actions possibles :
1. "ssh_cmd" : exécuter une commande bash sur le serveur distant. Fournis le "target" et la "command".
2. "finish" : QUAND le diagnostic est clair, ou que tu n'as plus rien à tester.

EXEMPLES DE RÉPONSES (Réponds UNIQUEMENT avec un seul bloc JSON) :

Pour lancer une commande :
{
	"type": "ssh_cmd",
	"target": "user@ip",
	"command": "journalctl -u apache2 -n 50 --no-pager"
}

Pour terminer l'investigation (OBLIGATOIRE QUAND TU AS FINI) :
{
	"type": "finish",
	"target": "",
	"command": ""
}
`, server, problem)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: fmt.Sprintf("Démarre l'investigation sur le serveur %s pour le problème : %s", server, problem)},
	}

	scratchpad := ""
	maxSteps := 10
	for step := 1; step <= maxSteps; step++ {
		fmt.Printf("[SysadminAgent] Étape d'investigation %d/%d...\n", step, maxSteps)

		resp, err := s.llmProvider.Generate(ctx, messages)
		if err != nil {
			return fmt.Sprintf("Erreur lors de la réflexion sysadmin : %v", err)
		}

		respClean := strings.TrimSpace(resp)
		// Clean JSON
		if strings.HasPrefix(respClean, "```json") {
			respClean = strings.TrimPrefix(respClean, "```json")
			respClean = strings.TrimSuffix(respClean, "```")
		} else if strings.HasPrefix(respClean, "```") {
			respClean = strings.TrimPrefix(respClean, "```")
			respClean = strings.TrimSuffix(respClean, "```")
		}
		respClean = strings.TrimSpace(respClean)

		jsonStart := strings.Index(respClean, "{")
		jsonEnd := strings.LastIndex(respClean, "}")
		if jsonStart != -1 && jsonEnd != -1 && jsonEnd > jsonStart {
			respClean = respClean[jsonStart : jsonEnd+1]
		}

		messages = append(messages, llm.Message{Role: llm.RoleAssistant, Content: respClean})

		var action SysadminAction
		err = json.Unmarshal([]byte(repairJSON(respClean)), &action)
		if err != nil {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "Erreur JSON. Réponds UNIQUEMENT en JSON avec 'type', 'target' et 'command'."})
			continue
		}

		if action.Type == "finish" {
			if scratchpad == "" {
				return "L'analyse a été complétée sans collecter de données."
			}
			return s.generateFinalReport(ctx, server, problem, scratchpad)
		}

		if action.Type == "ssh_cmd" {
			if action.Target == "" {
				action.Target = server
			}
			fmt.Printf("[SysadminAgent] Exécution SSH sur %s : %s\n", action.Target, action.Command)
			
			output := s.executeSSH(ctx, action.Target, action.Command)
			fmt.Printf("[SysadminAgent] Résultat obtenu (%d caractères).\n", len(output))
			
			scratchpad += fmt.Sprintf("### Commande: `%s`\n**Résultat:**\n```\n%s\n```\n\n", action.Command, output)
			
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf("Résultat de la commande: \n%s\n\nQue fais-tu maintenant ?", output)})
		} else {
			messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "Action inconnue. Utilise 'ssh_cmd' ou 'finish'."})
		}
	}

	return s.generateFinalReport(ctx, server, problem, scratchpad)
}

func (s *SysadminAgent) generateFinalReport(ctx context.Context, server string, problem string, scratchpad string) string {
	fmt.Println("[SysadminAgent] Rédaction du rapport de diagnostic final...")

	systemPrompt := "Tu es un Administrateur Système Senior. Ta mission est de rédiger un rapport d'incident détaillé basé sur les données brutes extraites d'un serveur."
	userPrompt := fmt.Sprintf(`Date du rapport : %s
Serveur cible : %s
Problème initial : "%s"

Voici les commandes exécutées et leurs résultats :
%s

RÈGLES D'OR ANT-HALLUCINATION :
1. N'invente **jamais** de logs ou d'incidents qui ne figurent pas dans les résultats ci-dessus.
2. Si le résultat indique "Permission denied" ou une erreur SSH, ton rapport DOIT simplement dire que tu n'as pas pu te connecter au serveur. Ne simule pas de diagnostic Apache dans ce cas !

Rédige un rapport Markdown structuré comprenant :
1. **Symptômes** : Ce qui a été observé.
2. **Diagnostic** : L'analyse technique (seulement si tu as pu lire les logs).
3. **Recommandations** : Les actions à entreprendre.
N'inclus que des informations pertinentes et vraies.`, time.Now().Format("2006-01-02"), server, problem, scratchpad)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	report, err := s.llmProvider.Generate(ctx, messages)
	if err != nil {
		fmt.Printf("[SysadminAgent] Erreur génération rapport : %v\n", err)
		return scratchpad
	}
	
	finalReport := strings.TrimSpace(report)
	finalReport += "\n\n---\n\n### 📜 Données Brutes Récupérées (Terminal)\n\n" + scratchpad
	
	return finalReport
}

func NewSysadminTaskHandler(agent *SysadminAgent, broadcaster EventBroadcaster, stm *memory.STM) scheduler.TaskHandler {
	return func(ctx context.Context, task *scheduler.Task) error {
		task.AppendLog(fmt.Sprintf("Démarrage du diagnostic SysAdmin sur : %s", task.Payload))

		// task.Payload could be "root@ip ||| problem description"
		parts := strings.Split(task.Payload, "|||")
		server := strings.TrimSpace(parts[0])
		problem := "Diagnostic général"
		if len(parts) > 1 {
			problem = strings.TrimSpace(parts[1])
		}

		synthesis := agent.RunDiagnosis(ctx, server, problem)

		task.AppendLog("Diagnostic terminé. Diffusion du rapport...")

		formattedResponse := fmt.Sprintf(`⚙️ **[Rapport SysAdmin : %s]**
*Investigation terminée concernant : %s*

---

%s`, server, problem, synthesis)

		broadcaster.Broadcast(formattedResponse)
		stm.AddMessage(llm.Message{Role: llm.RoleAssistant, Content: formattedResponse})
		return nil
	}
}
