package scheduler

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusCancelled TaskStatus = "cancelled"
)

// Task represents an asynchronous job in Pixel.
type Task struct {
	ID            string     `json:"id"`
	Type          string     `json:"type"`
	Name          string     `json:"name"`
	Status        TaskStatus `json:"status"`
	Payload       string     `json:"payload"`
	ScheduledAt   time.Time  `json:"scheduled_at"`
	StartedAt     time.Time  `json:"started_at,omitempty"`
	FinishedAt    time.Time  `json:"finished_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	Log           string     `json:"log,omitempty"`
	ResolvedURL   string     `json:"resolved_url,omitempty"`
	ResolvedTitle   string     `json:"resolved_title,omitempty"`
	IsDirect        bool       `json:"is_direct,omitempty"`
	Announcement    string     `json:"announcement,omitempty"`
	AnnouncementWav string     `json:"announcement_wav,omitempty"`
	Announced       bool       `json:"announced,omitempty"`
	TargetDevice    string     `json:"target_device,omitempty"` // "pc" (default, mpv PipeWire) or "android" / "mobile" (streamed to smartphone)

	// Private fields for execution control
	mu         sync.Mutex
	cancelFunc context.CancelFunc
	cmd        *exec.Cmd
	ReleaseSem func() `json:"-"`
}

func (t *Task) AppendLog(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	timestamp := time.Now().Format("15:04:05")
	t.Log += fmt.Sprintf("[%s] %s\n", timestamp, msg)
}

func (t *Task) SetStatus(status TaskStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = status
}

func (t *Task) SetError(errStr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Error = errStr
}

func (t *Task) SetCmd(cmd *exec.Cmd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cmd = cmd
}

func (t *Task) KillCmd() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Process.Kill()
		t.Log += fmt.Sprintf("[%s] Processus système interrompu.\n", time.Now().Format("15:04:05"))
	}
}

func (t *Task) GetStatus() TaskStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Status
}

func (t *Task) GetError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Error
}

func (t *Task) GetLog() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Log
}

func (t *Task) GetResolvedTitle() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ResolvedTitle
}

func (t *Task) SetAnnouncement(a string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Announcement = a
}

func (t *Task) GetAnnouncement() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Announcement
}

func (t *Task) SetAnnouncementWav(w string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.AnnouncementWav = w
}

func (t *Task) GetAnnouncementWav() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.AnnouncementWav
}

func (t *Task) SetAnnounced(v bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Announced = v
}

func (t *Task) IsAnnounced() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Announced
}

// VoiceSpeaker enables synthesizing and playing spoken announcements.
type VoiceSpeaker interface {
	SynthesizeToFile(ctx context.Context, text string, destWav string) error
	SpeakText(ctx context.Context, text string) error
}

type TaskHandler func(ctx context.Context, task *Task) error

type Scheduler struct {
	tasks       []*Task
	handlers    map[string]TaskHandler
	mu          sync.RWMutex
	ctx         context.Context
	Broadcaster EventBroadcaster

	// Serializer for music playing to avoid overlapping audio
	musicSem chan struct{}

	// Sync for mobile/android audio streaming
	streamSyncMu   sync.Mutex
	streamSyncDone map[string]chan struct{}

	// Voice speaker for radio DJ announcements
	voiceSpeaker VoiceSpeaker

	// Callback for Autoplay (Infinite Radio)
	AutoQueueCallback func(ctx context.Context, lastTitle string)

	autoplayCancel context.CancelFunc
	autoplayMu     sync.Mutex
}

func (s *Scheduler) SetVoiceSpeaker(sp VoiceSpeaker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.voiceSpeaker = sp
}

func (s *Scheduler) GetVoiceSpeaker() VoiceSpeaker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.voiceSpeaker
}

func (s *Scheduler) SetBroadcaster(b EventBroadcaster) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Broadcaster = b
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks:          make([]*Task, 0),
		handlers:       make(map[string]TaskHandler),
		musicSem:       make(chan struct{}, 1),
		streamSyncDone: make(map[string]chan struct{}),
	}
}

// RegisterStreamWait creates and registers a channel that unblocks when a mobile stream finishes.
func (s *Scheduler) RegisterStreamWait(taskID string) chan struct{} {
	s.streamSyncMu.Lock()
	defer s.streamSyncMu.Unlock()
	if s.streamSyncDone == nil {
		s.streamSyncDone = make(map[string]chan struct{})
	}
	ch := make(chan struct{})
	s.streamSyncDone[taskID] = ch
	return ch
}

// UnregisterStreamWait cleans up a wait channel.
func (s *Scheduler) UnregisterStreamWait(taskID string) {
	s.streamSyncMu.Lock()
	defer s.streamSyncMu.Unlock()
	if s.streamSyncDone != nil {
		delete(s.streamSyncDone, taskID)
	}
}

// NotifyStreamDone notifies that a mobile stream track has finished playing or was skipped/stopped.
func (s *Scheduler) NotifyStreamDone(taskID string) bool {
	s.streamSyncMu.Lock()
	defer s.streamSyncMu.Unlock()
	if s.streamSyncDone == nil {
		return false
	}
	if taskID == "" {
		notified := false
		for id, ch := range s.streamSyncDone {
			select {
			case <-ch:
			default:
				close(ch)
				notified = true
			}
			delete(s.streamSyncDone, id)
		}
		return notified
	}
	if ch, ok := s.streamSyncDone[taskID]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
		delete(s.streamSyncDone, taskID)
		return true
	}
	return false
}

// RegisterHandler registers a new task type runner.
func (s *Scheduler) RegisterHandler(taskType string, handler TaskHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[taskType] = handler
}

// TriggerAutoplay starts the autoplay callback with a cancellable context.
func (s *Scheduler) TriggerAutoplay(title string) {
	s.autoplayMu.Lock()
	if s.autoplayCancel != nil {
		s.autoplayCancel()
	}
	var ctx context.Context
	ctx, s.autoplayCancel = context.WithCancel(s.ctx)
	if ctx == nil {
		ctx, s.autoplayCancel = context.WithCancel(context.Background())
	}
	s.autoplayMu.Unlock()

	if s.AutoQueueCallback != nil {
		s.AutoQueueCallback(ctx, title)
	}
}

// CancelAutoplay cancels any ongoing autoplay generation.
func (s *Scheduler) CancelAutoplay() {
	s.autoplayMu.Lock()
	defer s.autoplayMu.Unlock()
	if s.autoplayCancel != nil {
		s.autoplayCancel()
		s.autoplayCancel = nil
	}
}

// Enqueue adds a new task to the queue.
func (s *Scheduler) Enqueue(taskType string, name string, payload string, delay time.Duration) *Task {
	return s.EnqueueWithDevice(taskType, name, payload, delay, "pc")
}

// EnqueueWithDevice adds a new task with a specified target execution device ("pc" or "android").
func (s *Scheduler) EnqueueWithDevice(taskType string, name string, payload string, delay time.Duration, targetDevice string) *Task {
	s.mu.Lock()
	defer s.mu.Unlock()

	if targetDevice == "" {
		targetDevice = "pc"
	}

	task := &Task{
		ID:           uuid.New().String(),
		Type:         taskType,
		Name:         name,
		Status:       StatusPending,
		Payload:      payload,
		TargetDevice: targetDevice,
		ScheduledAt:  time.Now().Add(delay),
	}
	task.AppendLog(fmt.Sprintf("Tâche créée pour '%s' et planifiée pour %s", targetDevice, task.ScheduledAt.Format("15:04:05")))
	s.tasks = append(s.tasks, task)
	return task
}

// Cancel terminates a pending or running task.
func (s *Scheduler) Cancel(taskID string) error {
	s.mu.Lock()
	task, err := s.getTaskByIDUnlocked(taskID)
	s.mu.Unlock()

	if err != nil {
		return err
	}

	task.mu.Lock()
	defer task.mu.Unlock()

	if task.Status == StatusCompleted || task.Status == StatusFailed || task.Status == StatusCancelled {
		return fmt.Errorf("la tâche est déjà dans un état terminal : %s", task.Status)
	}

	task.Status = StatusCancelled
	task.FinishedAt = time.Now()
	task.Log += fmt.Sprintf("[%s] Tâche annulée par l'utilisateur.\n", task.FinishedAt.Format("15:04:05"))

	// If running, trigger cancel context and kill system command
	if task.cancelFunc != nil {
		task.cancelFunc()
	}
	s.NotifyStreamDone(taskID)
	if task.cmd != nil && task.cmd.Process != nil {
		task.cmd.Process.Kill()
		task.Log += fmt.Sprintf("[%s] Processus mpv tué avec succès.\n", task.FinishedAt.Format("15:04:05"))
	}

	return nil
}

// ClearHistory removes completed, failed, or cancelled tasks from history.
func (s *Scheduler) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()

	activeTasks := make([]*Task, 0)
	for _, t := range s.tasks {
		if t.Status == StatusPending || t.Status == StatusRunning {
			activeTasks = append(activeTasks, t)
		}
	}
	s.tasks = activeTasks
}

// GetTasks returns all tasks in the queue.
func (s *Scheduler) GetTasks() []*Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy slice
	res := make([]*Task, len(s.tasks))
	copy(res, s.tasks)
	return res
}

// HasActiveTasks returns true if there is any task currently running.
func (s *Scheduler) HasActiveTasks() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tasks {
		if t.Status == StatusRunning {
			return true
		}
	}
	return false
}

func (s *Scheduler) getTaskByIDUnlocked(id string) (*Task, error) {
	for _, t := range s.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, fmt.Errorf("tâche non trouvée : %s", id)
}

// Start launches the background worker pool loop.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		fmt.Println("[Scheduler] Gestionnaire de tâches asynchrones démarré.")

		for {
			select {
			case <-ctx.Done():
				fmt.Println("[Scheduler] Arrêt du gestionnaire de tâches.")
				return
			case <-ticker.C:
				s.dispatchTasks(ctx)
			}
		}
	}()
}

func (s *Scheduler) dispatchTasks(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for _, task := range s.tasks {
		if task.Status == StatusPending && !task.ScheduledAt.After(now) {
			// Acquire execution details under read lock equivalent
			handler, ok := s.handlers[task.Type]
			if !ok {
				task.SetStatus(StatusFailed)
				task.FinishedAt = time.Now()
				task.SetError(fmt.Sprintf("aucun handler enregistré pour le type de tâche '%s'", task.Type))
				task.AppendLog(fmt.Sprintf("Échec : type de tâche inconnu '%s'", task.Type))
				continue
			}

			// Special serialization for music playing tasks: acquire musicSem in strict FIFO order
			// before transitioning to StatusRunning so queued tracks stay Pending until their turn.
			if task.Type == "play_music" {
				select {
				case s.musicSem <- struct{}{}:
					// Audio channel acquired in strict FIFO order
				default:
					if !strings.Contains(task.GetLog(), "Attente de libération du canal audio") {
						task.AppendLog("Attente de libération du canal audio (file d'attente)...")
					}
					continue
				}
			}

			taskCtx, cancel := context.WithCancel(ctx)
			task.mu.Lock()
			task.Status = StatusRunning
			task.StartedAt = now
			task.cancelFunc = cancel
			task.mu.Unlock()
			task.AppendLog("Lancement de l'exécution.")

			// Run task in background goroutine
			go s.runTask(taskCtx, cancel, task, handler)
		}
	}
}

func (s *Scheduler) runTask(taskCtx context.Context, cancel context.CancelFunc, task *Task, handler TaskHandler) {
	defer cancel()

	// Special serialization for music playing tasks (musicSem was acquired in dispatchTasks)
	if task.Type == "play_music" {
		var semReleased bool
		var semMu sync.Mutex

		task.ReleaseSem = func() {
			semMu.Lock()
			defer semMu.Unlock()
			if !semReleased {
				<-s.musicSem
				semReleased = true
				task.AppendLog("Canal audio libéré pour l'enchaînement direct du titre suivant.")
				go s.TriggerDispatch()
			}
		}
		defer task.ReleaseSem()

		if task.GetStatus() == StatusCancelled || taskCtx.Err() != nil {
			task.AppendLog("Tâche annulée en attente du canal audio.")
			return
		}

		task.AppendLog("Canal audio acquis. Préparation de la piste...")
	}

	err := handler(taskCtx, task)

	task.mu.Lock()
	defer task.mu.Unlock()

	// Only transition status if it wasn't cancelled by user during execution
	if task.Status == StatusRunning {
		task.FinishedAt = time.Now()
		s.mu.RLock()
		b := s.Broadcaster
		s.mu.RUnlock()
		if err != nil {
			task.Status = StatusFailed
			task.Error = err.Error()
			task.Log += fmt.Sprintf("[%s] Échec de l'exécution : %v\n", task.FinishedAt.Format("15:04:05"), err)
			if b != nil {
				b.Broadcast(fmt.Sprintf("❌ **Échec de la tâche** (*%s*) : %v", task.Name, err))
			}
		} else {
			task.Status = StatusCompleted
			task.Log += fmt.Sprintf("[%s] Exécution terminée avec succès.\n", task.FinishedAt.Format("15:04:05"))
			if b != nil && task.Type != "play_music" && task.Type != "research_task" {
				b.Broadcast(fmt.Sprintf("✅ **Tâche terminée avec succès** : %s 🚀", task.Name))
			}
		}
	}
}

// TriggerDispatch triggers task dispatching immediately.
func (s *Scheduler) TriggerDispatch() {
	s.mu.RLock()
	globalCtx := s.ctx
	s.mu.RUnlock()
	if globalCtx == nil {
		globalCtx = context.Background()
	}
	s.dispatchTasks(globalCtx)
}

// GetNextPendingMusicTask returns the next pending or waiting play_music task after currentTaskID.
func (s *Scheduler) GetNextPendingMusicTask(currentTaskID string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()

	foundCurrent := false
	for _, t := range s.tasks {
		if t.ID == currentTaskID {
			foundCurrent = true
			continue
		}
		if foundCurrent && t.Type == "play_music" && (t.Status == StatusPending || t.Status == StatusRunning) && t.ResolvedURL == "" {
			return t
		}
	}
	return nil
}

func clonePendingMusicTask(src *Task) *Task {
	src.mu.Lock()
	defer src.mu.Unlock()

	targetDevice := src.TargetDevice
	if targetDevice == "" {
		targetDevice = "pc"
	}
	t := &Task{
		ID:            uuid.New().String(),
		Type:          src.Type,
		Name:          src.Name,
		Status:        StatusPending,
		Payload:       src.Payload,
		TargetDevice:  targetDevice,
		ScheduledAt:   time.Now(),
		ResolvedURL:   src.ResolvedURL,
		ResolvedTitle: src.ResolvedTitle,
		IsDirect:      src.IsDirect,
		Announcement:  src.Announcement,
	}
	t.AppendLog(fmt.Sprintf("Tâche replanifiée (retour piste précédente) pour '%s'", targetDevice))
	return t
}

// StepBackMusicTask navigates to the previous play_music task in the queue,
// preserving the current track right after it so subsequent "next" commands
// or natural track endings continue in order.
func (s *Scheduler) StepBackMusicTask() (*Task, bool) {
	s.mu.Lock()

	currIdx := -1
	for i, t := range s.tasks {
		if t.Type == "play_music" && t.Status == StatusRunning {
			currIdx = i
			break
		}
	}

	// Find the most recent finished/cancelled play_music task before currIdx (or at the end if nothing is running)
	searchEnd := len(s.tasks)
	if currIdx >= 0 {
		searchEnd = currIdx
	}

	prevIdx := -1
	for i := searchEnd - 1; i >= 0; i-- {
		t := s.tasks[i]
		if t.Type == "play_music" && (t.Status == StatusCompleted || t.Status == StatusCancelled || t.Status == StatusFailed) {
			prevIdx = i
			break
		}
	}

	// If there is no previous track before the running track, restart the running track from the beginning
	if prevIdx == -1 && currIdx >= 0 {
		currTask := s.tasks[currIdx]
		restarted := clonePendingMusicTask(currTask)
		newTasks := make([]*Task, 0, len(s.tasks))
		newTasks = append(newTasks, s.tasks[:currIdx]...)
		newTasks = append(newTasks, restarted)
		newTasks = append(newTasks, s.tasks[currIdx+1:]...)
		s.tasks = newTasks
		s.mu.Unlock()

		_ = s.Cancel(currTask.ID)
		go s.TriggerDispatch()
		return restarted, true
	}

	if prevIdx == -1 {
		s.mu.Unlock()
		return nil, false
	}

	prevTask := s.tasks[prevIdx]
	prevClone := clonePendingMusicTask(prevTask)

	var currTask *Task
	var currClone *Task
	if currIdx >= 0 {
		currTask = s.tasks[currIdx]
		currClone = clonePendingMusicTask(currTask)
	}

	newTasks := make([]*Task, 0, len(s.tasks)+1)
	for i, t := range s.tasks {
		if i == prevIdx {
			continue
		}
		if i == currIdx {
			newTasks = append(newTasks, prevClone)
			if currClone != nil {
				newTasks = append(newTasks, currClone)
			}
			continue
		}
		newTasks = append(newTasks, t)
	}
	if currIdx == -1 {
		newTasks = append(newTasks, prevClone)
	}
	s.tasks = newTasks
	s.mu.Unlock()

	if currTask != nil {
		_ = s.Cancel(currTask.ID)
	}
	go s.TriggerDispatch()
	return prevClone, true
}

