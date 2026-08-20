package resourceagent

import (
	"runtime"
	"time"

	"github.com/marce555/pixel/internal/memory"
)

// ResourceAgent monitors the system's memory and "energy" (token usage, RAM).
type ResourceAgent struct {
	stm *memory.STM
}

func NewResourceAgent(stm *memory.STM) *ResourceAgent {
	return &ResourceAgent{
		stm: stm,
	}
}

// CheckHealth returns true if the system is healthy, false if it needs sleep.
// For example, if STM is too full, it might force a sleep cycle.
func (r *ResourceAgent) CheckHealth() bool {
	// 1. Check STM size
	if r.stm.IsFull() {
		return false // Needs sleep to consolidate
	}

	// 2. Comportement organique : S'endort après 15 minutes de silence si la mémoire n'est pas vide
	if len(r.stm.GetMessages()) > 0 && time.Since(r.stm.GetLastActivity()) > 15*time.Minute {
		return false
	}

	// 2. Check system RAM as a mock "energy" metric
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	// If Go is using more than 1GB of RAM, we might want to trigger GC/Sleep
	if m.Alloc > 1024*1024*1024 {
		return false
	}

	return true
}
