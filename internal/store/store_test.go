package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestClaimTakesEachTaskOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		s.CreateTask(ctx, "goal")
	}

	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				task, ok, err := s.Claim(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				seen[task.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != 100 {
		t.Fatalf("claimed %d tasks, want 100", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("task %d claimed %d times", id, n)
		}
	}
}

func TestWaitingWakesAtDeadline(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	s.CreateTask(ctx, "goal")
	task, _, _ := s.Claim(ctx)

	task.Status = Waiting
	task.Deadline = time.Now().Add(time.Hour)
	s.Finish(ctx, task)
	if _, ok, _ := s.Claim(ctx); ok {
		t.Fatal("claimed before deadline")
	}

	s.db.Exec(`UPDATE tasks SET deadline = datetime('now', '-1 second')`)
	if _, ok, _ := s.Claim(ctx); !ok {
		t.Fatal("not claimed after deadline")
	}
}

func TestFinishDoesNotOverwriteCancel(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	id, _ := s.CreateTask(ctx, "goal")
	task, _, _ := s.Claim(ctx)
	s.Cancel(ctx, id)

	task.Status = Done
	if ok, err := s.Finish(ctx, task); err != nil || ok {
		t.Fatalf("Finish after cancel: ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestClaimReturnsEvents(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	id, _ := s.CreateTask(ctx, "goal")
	s.AddEvent(ctx, id, "note", "написал в ресторан")

	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if len(task.Events) != 2 || task.Events[1].Body != "написал в ресторан" {
		t.Fatalf("events = %+v, want goal + note", task.Events)
	}
}
