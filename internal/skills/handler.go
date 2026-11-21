package skills

import (
	"context"

	"github.com/marce555/pixel/internal/scheduler"
)

type funcBroadcaster func(string)

func (f funcBroadcaster) Broadcast(msg string) {
	if f != nil {
		f(msg)
	}
}

// NewBuildSkillHandler returns a TaskHandler for asynchronous generation of a new skill.
func NewBuildSkillHandler(sm *SkillManager, broadcast func(string)) scheduler.TaskHandler {
	return NewBuildSkillHandlerWithBroadcaster(sm, funcBroadcaster(broadcast))
}

// NewBuildSkillHandlerWithBroadcaster returns a device-aware TaskHandler for asynchronous skill creation.
func NewBuildSkillHandlerWithBroadcaster(sm *SkillManager, broadcaster scheduler.EventBroadcaster) scheduler.TaskHandler {
	return func(ctx context.Context, task *scheduler.Task) error {
		// The task.Payload contains the user's query/description of the skill.
		scheduler.BroadcastForTask(broadcaster, task, "⏳ Pixel concentrée sur le développement d'une nouvelle brique...")

		result, skillName, err := sm.BuildSkill(ctx, task.Payload)
		if err != nil {
			scheduler.BroadcastFailureForTask(broadcaster, task, "❌ Échec de la création de la brique : "+err.Error())
			return err
		}

		scheduler.BroadcastForTask(broadcaster, task, "✅ "+result)

		// Exécution automatique de la brique fraîchement créée pour afficher directement le résultat à l'utilisateur
		scheduler.BroadcastForTask(broadcaster, task, "🔄 Exécution automatique de la nouvelle brique...")
		execResult, execErr := sm.ExecuteSkill(ctx, skillName, task.Payload)
		if execErr != nil {
			scheduler.BroadcastForTask(broadcaster, task, "⚠️ L'exécution automatique a échoué : "+execErr.Error())
		} else {
			scheduler.BroadcastForTask(broadcaster, task, "📊 **Résultat de l'analyse :**\n"+execResult)
		}

		return nil
	}
}
