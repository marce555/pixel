package skills

import (
	"context"

	"github.com/marce555/pixel/internal/scheduler"
)

// NewBuildSkillHandler returns a TaskHandler for asynchronous generation of a new skill.
func NewBuildSkillHandler(sm *SkillManager, broadcast func(string)) scheduler.TaskHandler {
	return func(ctx context.Context, task *scheduler.Task) error {
		// The task.Payload contains the user's query/description of the skill.
		broadcast("⏳ Pixel concentrée sur le développement d'une nouvelle brique...")
		
		result, skillName, err := sm.BuildSkill(ctx, task.Payload)
		if err != nil {
			broadcast("❌ Échec de la création de la brique : " + err.Error())
			return err
		}
		
		broadcast("✅ " + result)

		// Exécution automatique de la brique fraîchement créée pour afficher directement le résultat à l'utilisateur
		broadcast("🔄 Exécution automatique de la nouvelle brique...")
		execResult, execErr := sm.ExecuteSkill(ctx, skillName, task.Payload)
		if execErr != nil {
			broadcast("⚠️ L'exécution automatique a échoué : " + execErr.Error())
		} else {
			broadcast("📊 **Résultat de l'analyse :**\n" + execResult)
		}
		
		return nil
	}
}
