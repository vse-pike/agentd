package store

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// open подключается к тестовой базе из TEST_DATABASE_URL (чистый лист для каждого
// теста) или пропускает тест, если переменная не задана.
func open(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL не задан — нет тестовой базы")
	}
	s, err := Open(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.db.Exec(`TRUNCATE events, tasks RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
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

	s.db.Exec(`UPDATE tasks SET deadline = now() - interval '1 second'`)
	if _, ok, _ := s.Claim(ctx); !ok {
		t.Fatal("not claimed after deadline")
	}
}

func TestResetRunningReturnsToQueue(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	s.CreateTask(ctx, "goal")
	if _, ok, _ := s.Claim(ctx); !ok {
		t.Fatal("not claimed")
	}
	if err := s.ResetRunning(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Claim(ctx); !ok {
		t.Fatal("not reclaimed after reset")
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
