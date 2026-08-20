package timeagent

import (
	"fmt"
	"time"
)

// TimeAgent is responsible for giving temporal awareness to the Superior Agent.
type TimeAgent struct {
	bootTime time.Time
}

// NewTimeAgent initializes the temporal awareness.
func NewTimeAgent() *TimeAgent {
	return &TimeAgent{
		bootTime: time.Now(),
	}
}

// GetCurrentTimeContext returns a prompt-friendly string describing the current time and duration.
func (t *TimeAgent) GetCurrentTimeContext() string {
	now := time.Now()
	duration := now.Sub(t.bootTime)
	
	return fmt.Sprintf("Contexte Temporel : Il est actuellement %s. Nous discutons depuis %s.",
		now.Format("15:04:05 (02 Jan 2006)"),
		duration.Round(time.Minute).String(),
	)
}

// ShouldSleep returns true if the system should enter a sleep cycle (e.g. night time).
func (t *TimeAgent) ShouldSleep() bool {
	hour := time.Now().Hour()
	// Silence absolu entre 23h et 7h du matin
	if hour >= 23 || hour < 7 {
		return true
	}
	return false
}

// GetTimeOfDay returns a string representing the general time of day
func (t *TimeAgent) GetTimeOfDay() string {
	hour := time.Now().Hour()
	if hour >= 7 && hour < 12 {
		return "matin"
	} else if hour >= 12 && hour < 18 {
		return "après-midi"
	} else if hour >= 18 && hour < 23 {
		return "soir"
	}
	return "nuit"
}
