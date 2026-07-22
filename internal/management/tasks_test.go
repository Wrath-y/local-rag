package management

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTaskManagerQueuesConflictingWorkAndRecordsFailure(t *testing.T) {
	manager := NewTaskManager(10)
	started := make(chan struct{})
	release := make(chan struct{})
	first, err := manager.Submit("first", func(*TaskReporter) (map[string]any, error) {
		close(started)
		<-release
		return map[string]any{"done": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := manager.Submit("second", func(*TaskReporter) (map[string]any, error) { return nil, errors.New("expected failure") })
	if err != nil {
		t.Fatal(err)
	}
	if task, ok := manager.Get(second.ID); !ok || task.State != TaskQueued {
		t.Fatalf("second task = %#v, %v; want queued", task, ok)
	}
	close(release)
	waitFor(t, func() bool { task, ok := manager.Get(first.ID); return ok && task.State == TaskSucceeded })
	waitFor(t, func() bool { task, ok := manager.Get(second.ID); return ok && task.State == TaskFailed })
	if task, _ := manager.Get(second.ID); task.Error != "expected failure" {
		t.Fatalf("failure error = %q", task.Error)
	}
}

func TestTaskManagerIDsAreOpaqueAndUnique(t *testing.T) {
	manager := NewTaskManager(100)
	var wg sync.WaitGroup
	ids := make(chan string, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := manager.Submit("test", func(*TaskReporter) (map[string]any, error) { return nil, nil })
			if err != nil {
				t.Error(err)
				return
			}
			ids <- task.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("invalid or duplicate task id %q", id)
		}
		seen[id] = true
	}
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
