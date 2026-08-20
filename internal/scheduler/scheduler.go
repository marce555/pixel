package scheduler

import (
	"context"
	"fmt"
	"os/exec"
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
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Status      TaskStatus `json:"status"`
	Payload     string     `json:"payload"`
	ScheduledAt time.Time  `json:"scheduled_at"`
	StartedAt   time.Time  `json:"started_at,omitempty"`
	FinishedAt  time.Time  `json:"finished_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	Log         string     `json:"log,omitempty"`
	ResolvedURL   string     `json:"resolved_url,omitempty"`
	ResolvedTitle string     `json:"resolved_title,omitempty"`
	IsDirect      bool       `json:"is_direct,omitempty"`

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

type TaskHandler func(ctx context.Context, task *Task) error

type Scheduler struct {
	tasks       []*Task
	handlers    map[string]TaskHandler
	mu          sync.RWMutex
	ctx         context.Context
	Broadcaster EventBroadcaster
	
	// Serializer for music playing to avoid overlapping audio
	musicSem chan struct{}
	
	// Callback for Autoplay (Infinite Radio)
	AutoQueueCallback func(ctx context.Context, lastTitle string)

	autoplayCancel context.CancelFunc
	autoplayMu     sync.Mutex
}

func (s *Scheduler) SetBroadcaster(b EventBroadcaster) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Broadcaster = b
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks:    make([]*Task, 0),
		handlers: make(map[string]TaskHandler),
		musicSem: make(chan struct{}, 1),
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()

	task := &Task{
		ID:          uuid.New().String(),
		Type:        taskType,
		Name:        name,
		Status:      StatusPending,
		Payload:     payload,
		ScheduledAt: time.Now().Add(delay),
	}
	task.AppendLog(fmt.Sprintf("Tâche créée et planifiée pour %s", task.ScheduledAt.Format("15:04:05")))
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
			task.SetStatus(StatusRunning)
			task.StartedAt = now
			task.AppendLog("Lancement de l'exécution.")

			// Acquire execution details under read lock equivalent
			handler, ok := s.handlers[task.Type]
			if !ok {
				task.SetStatus(StatusFailed)
				task.FinishedAt = time.Now()
				task.SetError(fmt.Sprintf("aucun handler enregistré pour le type de tâche '%s'", task.Type))
				task.AppendLog(fmt.Sprintf("Échec : type de tâche inconnu '%s'", task.Type))
				continue
			}

			// Run task in background goroutine
			go s.runTask(ctx, task, handler)
		}
	}
}

func (s *Scheduler) runTask(parentCtx context.Context, task *Task, handler TaskHandler) {
	taskCtx, cancel := context.WithCancel(parentCtx)
	
	task.mu.Lock()
	task.cancelFunc = cancel
	task.mu.Unlock()

	defer cancel()

	// Special serialization for music playing tasks
	if task.Type == "play_music" {
		task.AppendLog("Attente de libération du canal audio (file d'attente)...")
		select {
		case s.musicSem <- struct{}{}:
			var semReleased bool
			var semMu sync.Mutex
			
			task.ReleaseSem = func() {
				semMu.Lock()
				defer semMu.Unlock()
				if !semReleased {
					<-s.musicSem
					semReleased = true
					task.AppendLog("Canal audio libéré par anticipation (crossfade).")
					go s.TriggerDispatch()
				}
			}
			defer task.ReleaseSem()

			task.AppendLog("Canal audio acquis. Lecture en cours.")
		case <-taskCtx.Done():
			task.SetStatus(StatusCancelled)
			task.FinishedAt = time.Now()
			task.AppendLog("Tâche annulée en attente du canal audio.")
			return
		}
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
			if b != nil && task.Type != "play_music" {
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

