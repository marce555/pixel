package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSchedulerEnqueueAndDispatch(t *testing.T) {
	s := NewScheduler()

	executed := make(chan bool, 1)
	s.RegisterHandler("test_job", func(ctx context.Context, task *Task) error {
		task.AppendLog("Job executed!")
		executed <- true
		return nil
	})

	task := s.Enqueue("test_job", "Test Job Name", "test_payload", 0)
	if task.Status != StatusPending {
		t.Fatalf("expected pending status, got %s", task.Status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Start(ctx)

	select {
	case <-executed:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for task execution")
	}

	// Wait slightly for the worker to update task status
	time.Sleep(100 * time.Millisecond)

	tasks := s.GetTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task in queue, got %d", len(tasks))
	}

	if tasks[0].Status != StatusCompleted {
		t.Fatalf("expected completed status, got %s", tasks[0].Status)
	}
}

func TestSchedulerTaskFailure(t *testing.T) {
	s := NewScheduler()

	s.RegisterHandler("fail_job", func(ctx context.Context, task *Task) error {
		return errors.New("something went wrong")
	})

	s.Enqueue("fail_job", "Fail Job Name", "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Start(ctx)

	// Wait for dispatch and execution
	time.Sleep(1200 * time.Millisecond)

	tasks := s.GetTasks()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	if tasks[0].Status != StatusFailed {
		t.Fatalf("expected failed status, got %s", tasks[0].Status)
	}

	if tasks[0].Error != "something went wrong" {
		t.Fatalf("expected specific error message, got '%s'", tasks[0].Error)
	}
}

func TestSchedulerCancel(t *testing.T) {
	s := NewScheduler()

	s.RegisterHandler("long_job", func(ctx context.Context, task *Task) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})

	task := s.Enqueue("long_job", "Long Job Name", "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Start(ctx)

	// Wait for job to start running
	time.Sleep(1100 * time.Millisecond)

	if task.Status != StatusRunning {
		t.Fatalf("expected task to be running, got %s", task.Status)
	}

	err := s.Cancel(task.ID)
	if err != nil {
		t.Fatalf("failed to cancel task: %v", err)
	}

	if task.Status != StatusCancelled {
		t.Fatalf("expected cancelled status, got %s", task.Status)
	}
}
