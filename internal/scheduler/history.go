package scheduler

// GetRecentMusicHistory returns the payloads (titles) of the last completed or running music tasks.
func (s *Scheduler) GetRecentMusicHistory(count int) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var history []string
	for i := len(s.tasks) - 1; i >= 0; i-- {
		t := s.tasks[i]
		if t.Type == "play_music" {
			title := t.ResolvedTitle
			if title == "" {
				title = t.Payload
			}
			if title != "" {
				history = append(history, title)
			}
			if len(history) >= count {
				break
			}
		}
	}
	return history
}
