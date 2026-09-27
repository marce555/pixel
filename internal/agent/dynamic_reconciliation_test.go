package agent

import (
	"context"
	"os"
	"testing"

	"github.com/marce555/pixel/internal/memory"
)

func TestDynamicReconciler(t *testing.T) {
	testFilePath := "test_dynamic_reconciliation_core.json"
	defer os.Remove(testFilePath)

	cm := memory.NewCoreMemory(testFilePath)
	cm.UpdateDynamicGoals([]memory.DynamicGoal{
		{
			ID:          "deployment-critical-failure",
			Description: "Examiner immédiatement les causes racines pour l'échec critique du déploiement en production lié à SSH",
			Priority:    0.95,
			Status:      "active",
		},
		{
			ID:          "philosophy-ai-ethics",
			Description: "Évaluer les implications éthiques et philosophiques d'une IA",
			Priority:    0.8,
			Status:      "active",
		},
	})
	cm.UpdateVolatileState("Tâche immédiate", "Diagnostic SSH timeout")
	cm.UpdateVolatileState("Disponibilité", "Occupé")

	reconciler := NewDynamicReconciler(cm, nil, nil)

	// Test 1: Irrelevant statement
	reconciled, _ := reconciler.ReconcileOnUserInput(context.Background(), "Quel temps fait-il aujourd'hui ?")
	if reconciled {
		t.Errorf("Ne devrait pas déclencher de réconciliation sur une question météo")
	}

	// Verify goals unchanged
	goals := cm.GetDynamicGoals()
	if goals[0].Status != "active" {
		t.Errorf("Le but SSH devrait toujours être actif")
	}

	// Test 2: Resolution statement: "Le problème SSH est résolu"
	reconciled, logMsg := reconciler.ReconcileOnUserInput(context.Background(), "Le problème SSH est résolu, tout refonctionne.")
	if !reconciled {
		t.Fatalf("Aurait dû déclencher la réconciliation pour le problème SSH résolu")
	}
	t.Logf("Reconciliation log: %s", logMsg)

	// Verify that SSH dynamic goal is now completed!
	goals = cm.GetDynamicGoals()
	if goals[0].Status != "completed" {
		t.Errorf("Le but SSH devrait être 'completed', reçu : %s", goals[0].Status)
	}
	if goals[1].Status != "active" {
		t.Errorf("Le but philosophique devrait rester 'active'")
	}

	// Verify that volatile "Tâche immédiate" is purged
	prof := cm.GetProfile()
	if _, exists := prof.Volatile["Tâche immédiate"]; exists {
		t.Errorf("La clé 'Tâche immédiate' aurait dû être supprimée du profil volatil")
	}
	if prof.Volatile["Disponibilité"] != "Disponible" {
		t.Errorf("La disponibilité aurait dû être remise à 'Disponible', reçu: %s", prof.Volatile["Disponibilité"])
	}
}
