package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/marce555/pixel/internal/memory"
)

// DynamicReconciler handles human-like real-time updates when an interlocutor
// declares that an issue, bug, task, or goal is resolved, completed, or no longer relevant.
type DynamicReconciler struct {
	coreMemory    *memory.CoreMemory
	ltm           *memory.LTM
	selfAwareness *SelfAwarenessAgent
}

func NewDynamicReconciler(coreMem *memory.CoreMemory, ltm *memory.LTM, sa *SelfAwarenessAgent) *DynamicReconciler {
	return &DynamicReconciler{
		coreMemory:    coreMem,
		ltm:           ltm,
		selfAwareness: sa,
	}
}

// ResolutionSignal represents a detected resolution of an ongoing topic or task.
type ResolutionSignal struct {
	IsResolution bool
	Keywords     []string
	RawTopic     string
}

// DetectResolutionSignal detects if a user's statement expresses that something is solved, completed, or obsolete.
func DetectResolutionSignal(input string) ResolutionSignal {
	clean := strings.ToLower(input)

	resolutionTriggers := []string{
		"résolu", "resolu", "a été résolu", "a ete resolu", "a été resolut", "a ete resolut",
		"réglé", "regle", "c'est regle", "c'est réglé", "est réglé", "est regle",
		"terminé", "termine", "c'est terminé", "c'est termine",
		"fini", "c'est fini",
		"clos", "c'est clos",
		"réparé", "repare", "c'est réparé",
		"oublie", "oublie ça", "oublie le", "oublie la",
		"plus d'actualité", "plus d actualite",
		"plus un problème", "plus un probleme", "plus un souci",
		"ne t'inquiète plus", "ne t inquiète plus", "t'inquiète pas", "t inquiete pas",
		"ce n'est plus important", "c'est plus important", "pas important",
		"je ne travaille plus", "on a fini", "tout refonctionne", "marche à nouveau", "marche a nouveau",
		"accès rétabli", "acces retabli", "connexion rétablie", "connexion retablie",
	}

	foundTrigger := false
	for _, trigger := range resolutionTriggers {
		if strings.Contains(clean, trigger) {
			foundTrigger = true
			break
		}
	}

	if !foundTrigger {
		return ResolutionSignal{IsResolution: false}
	}

	// Extract meaningful keywords (words >= 3 chars, ignoring common stopwords)
	words := strings.FieldsFunc(clean, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == '\'' || r == '"' || r == '!' || r == '?' || r == ';' || r == ':' || r == '(' || r == ')'
	})

	stopwords := map[string]bool{
		"le": true, "la": true, "les": true, "un": true, "une": true, "des": true,
		"du": true, "de": true, "d": true, "l": true, "ce": true, "cet": true, "cette": true,
		"est": true, "c": true, "a": true, "pour": true, "par": true, "avec": true, "sans": true,
		"et": true, "ou": true, "mais": true, "donc": true, "or": true, "ni": true, "car": true,
		"que": true, "qui": true, "quoi": true, "dont": true, "dans": true, "sur": true, "sous": true,
		"mon": true, "ton": true, "son": true, "notre": true, "votre": true, "leur": true,
		"plus": true, "pas": true, "ne": true, "en": true, "au": true, "aux": true,
		"tout": true, "tous": true, "bien": true, "fait": true, "été": true, "ete": true,
		"résolu": true, "resolu": true, "resolut": true, "réglé": true, "regle": true, "terminé": true, "termine": true,
		"fini": true, "clos": true, "oublie": true, "problème": true, "probleme": true, "souci": true,
	}

	var meaningfulKeywords []string
	for _, w := range words {
		if len(w) >= 3 && !stopwords[w] {
			meaningfulKeywords = append(meaningfulKeywords, w)
		}
	}

	return ResolutionSignal{
		IsResolution: true,
		Keywords:     meaningfulKeywords,
		RawTopic:     input,
	}
}

// ReconcileOnUserInput analyzes the user message in real time and applies instant reconciliation
// to active dynamic goals, volatile memory, and self-knowledge.
func (d *DynamicReconciler) ReconcileOnUserInput(ctx context.Context, input string) (bool, string) {
	if d.coreMemory == nil {
		return false, ""
	}

	signal := DetectResolutionSignal(input)
	if !signal.IsResolution {
		return false, ""
	}

	profile := d.coreMemory.GetProfile()
	var actionsTaken []string

	// 1. Check and resolve active dynamic goals
	activeGoals := d.coreMemory.GetDynamicGoals()
	for _, goal := range activeGoals {
		if goal.Status != "active" {
			continue
		}

		goalText := strings.ToLower(goal.Description + " " + goal.ID + " " + goal.Source)
		matches := false

		// Direct keyword match
		for _, kw := range signal.Keywords {
			if strings.Contains(goalText, kw) {
				matches = true
				break
			}
		}

		// Or if user message directly mentions words from goal ID / description
		if !matches {
			goalWords := strings.FieldsFunc(strings.ToLower(goal.Description), func(r rune) bool {
				return r == ' ' || r == '-' || r == '_' || r == '\'' || r == '"' || r == '(' || r == ')'
			})
			for _, gw := range goalWords {
				if len(gw) >= 4 && strings.Contains(strings.ToLower(input), gw) {
					matches = true
					break
				}
			}
		}

		if matches {
			d.coreMemory.SetDynamicGoalStatus(goal.ID, "completed")
			actionsTaken = append(actionsTaken, fmt.Sprintf("Objectif dynamique '%s' marqué complété", goal.ID))
		}
	}

	// 2. Check and clean volatile profile (e.g. "Tâche immédiate", "Projet en cours")
	for k, v := range profile.Volatile {
		valLower := strings.ToLower(v)
		matches := false

		for _, kw := range signal.Keywords {
			if strings.Contains(valLower, kw) {
				matches = true
				break
			}
		}

		if !matches && len(signal.Keywords) == 0 {
			// If user just said "c'est réglé" or "problème résolu" and key is "Tâche immédiate"
			if k == "Tâche immédiate" {
				matches = true
			}
		}

		if matches {
			d.coreMemory.RemoveVolatileState(k)
			actionsTaken = append(actionsTaken, fmt.Sprintf("Clé volatile '%s' (%s) purgée", k, v))
		}
	}

	// If immediate task was removed and availability was "Occupé", reset to "Disponible"
	if profile.Volatile["Disponibilité"] == "Occupé" {
		d.coreMemory.UpdateVolatileState("Disponibilité", "Disponible")
		actionsTaken = append(actionsTaken, "Disponibilité remise à 'Disponible'")
	}

	// 3. Register milestone in SelfAwareness and note resolution in LTM
	if len(actionsTaken) > 0 {
		resolutionSummary := fmt.Sprintf("Marcelo a confirmé que le sujet '%s' est résolu et clos.", strings.TrimSpace(input))
		if d.selfAwareness != nil {
			_ = d.selfAwareness.RecordLearning(ctx, SelfLearningEvent{
				Topic:      "Résolution",
				Summary:    resolutionSummary,
				Source:     "conversation",
				Category:   "Personal",
				Importance: 0.9,
				Tags:       append(signal.Keywords, "résolution", "clos"),
			})
		}

		resultLog := strings.Join(actionsTaken, ", ")
		fmt.Printf("[DynamicReconciler] ⚡ Réconciliation dynamique en temps réel appliquée : %s\n", resultLog)
		return true, resultLog
	}

	return false, ""
}
