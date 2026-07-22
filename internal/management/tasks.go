// Package management contains transport-neutral knowledge-base maintenance
// operations shared by the HTTP and MCP adapters.
package management

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"
)

type TaskState string

const (
	TaskQueued    TaskState = "queued"
	TaskRunning   TaskState = "running"
	TaskSucceeded TaskState = "succeeded"
	TaskFailed    TaskState = "failed"
)

// Task is an in-process, deliberately non-durable operation record.
type Task struct {
	ID          string         `json:"task_id"`
	Operation   string         `json:"operation"`
	State       TaskState      `json:"state"`
	SubmittedAt time.Time      `json:"submitted_at"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Progress    float64        `json:"progress,omitempty"`
	Processed   int            `json:"processed,omitempty"`
	Total       int            `json:"total,omitempty"`
	Message     string         `json:"message,omitempty"`
	Result      map[string]any `json:"result,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type taskRecord struct {
	task Task
	done chan struct{}
}

// TaskManager serializes submitted mutations. Records are kept only in memory
// and the oldest terminal records are discarded after MaxRecords is reached.
type TaskManager struct {
	mu         sync.RWMutex
	mutationMu sync.Mutex
	records    map[string]*taskRecord
	maxRecords int
}

func NewTaskManager(maxRecords int) *TaskManager {
	if maxRecords <= 0 {
		maxRecords = 100
	}
	return &TaskManager{records: make(map[string]*taskRecord), maxRecords: maxRecords}
}

type TaskReporter struct {
	manager *TaskManager
	id      string
}

func (r *TaskReporter) Progress(progress float64, message string) {
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	r.manager.mu.Lock()
	defer r.manager.mu.Unlock()
	if record := r.manager.records[r.id]; record != nil && record.task.State == TaskRunning {
		record.task.Progress, record.task.Message = progress, message
	}
}

// ProgressCounts adds index-style work counts to the normal progress update.
func (r *TaskReporter) ProgressCounts(progress float64, processed, total int, message string) {
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}
	r.manager.mu.Lock()
	defer r.manager.mu.Unlock()
	if record := r.manager.records[r.id]; record != nil && record.task.State == TaskRunning {
		record.task.Progress, record.task.Processed, record.task.Total, record.task.Message = progress, processed, total, message
	}
}

// Submit returns immediately. Work begins only after prior submitted mutation
// reaches a terminal state, allowing callers to observe queued work.
func (m *TaskManager) Submit(operation string, work func(*TaskReporter) (map[string]any, error)) (Task, error) {
	if operation == "" {
		return Task{}, fmt.Errorf("task operation is required")
	}
	id, err := newTaskID()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	task := Task{ID: id, Operation: operation, State: TaskQueued, SubmittedAt: now}
	m.mu.Lock()
	m.records[id] = &taskRecord{task: task, done: make(chan struct{})}
	m.pruneLocked()
	m.mu.Unlock()
	go m.run(id, work)
	return task, nil
}

func (m *TaskManager) run(id string, work func(*TaskReporter) (map[string]any, error)) {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	now := time.Now().UTC()
	m.mu.Lock()
	record := m.records[id]
	if record == nil {
		m.mu.Unlock()
		return
	}
	record.task.State, record.task.StartedAt = TaskRunning, &now
	m.mu.Unlock()

	result, err := work(&TaskReporter{manager: m, id: id})
	completed := time.Now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	record = m.records[id]
	if record == nil {
		return
	}
	record.task.CompletedAt = &completed
	if err != nil {
		record.task.State, record.task.Error = TaskFailed, safeError(err)
		close(record.done)
		return
	}
	record.task.State, record.task.Progress, record.task.Result = TaskSucceeded, 1, cloneMap(result)
	close(record.done)
}

// Wait is used by synchronous transports that preserve an existing immediate
// response while still invoking the same queued management workflow.
func (m *TaskManager) Wait(ctx context.Context, id string) (Task, bool, error) {
	m.mu.RLock()
	record := m.records[id]
	if record == nil {
		m.mu.RUnlock()
		return Task{}, false, nil
	}
	done := record.done
	m.mu.RUnlock()
	select {
	case <-done:
	case <-ctx.Done():
		return Task{}, true, ctx.Err()
	}
	task, found := m.Get(id)
	return task, found, nil
}

func (m *TaskManager) Get(id string) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record := m.records[id]
	if record == nil {
		return Task{}, false
	}
	return cloneTask(record.task), true
}

func (m *TaskManager) pruneLocked() {
	if len(m.records) <= m.maxRecords {
		return
	}
	terminal := make([]Task, 0, len(m.records))
	for _, record := range m.records {
		if record.task.State == TaskSucceeded || record.task.State == TaskFailed {
			terminal = append(terminal, record.task)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].SubmittedAt.Before(terminal[j].SubmittedAt) })
	for len(m.records) > m.maxRecords && len(terminal) > 0 {
		delete(m.records, terminal[0].ID)
		terminal = terminal[1:]
	}
}

func cloneTask(task Task) Task { task.Result = cloneMap(task.Result); return task }
func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	copy := make(map[string]any, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}
func safeError(err error) string { return err.Error() }
func newTaskID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
