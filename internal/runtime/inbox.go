package runtime

import (
	"sync"
	"time"
)

// TaskResult holds the outcome of a delegated specialist task.
type TaskResult struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"` // "pending", "complete", "error"
	Content   string `json:"content,omitempty"`
	Error     string `json:"error,omitempty"`
	UpdatedAt time.Time
}

// Inbox is a concurrency-safe in-memory store for specialist task results.
// The orchestrator marks tasks as pending on dispatch and updates them when
// results arrive over NATS. GetTaskResult reads from this inbox.
type Inbox struct {
	mu      sync.RWMutex
	results map[string]*TaskResult
}

// NewInbox creates an empty Inbox.
func NewInbox() *Inbox {
	return &Inbox{results: make(map[string]*TaskResult)}
}

// SetPending records a task as dispatched but not yet complete.
func (in *Inbox) SetPending(taskID string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.results[taskID] = &TaskResult{
		TaskID:    taskID,
		Status:    "pending",
		UpdatedAt: time.Now(),
	}
}

// Put stores or overwrites a task result in the inbox.
func (in *Inbox) Put(result *TaskResult) {
	in.mu.Lock()
	defer in.mu.Unlock()
	result.UpdatedAt = time.Now()
	in.results[result.TaskID] = result
}

// Get returns the current result for the given task ID, or nil if unknown.
func (in *Inbox) Get(taskID string) *TaskResult {
	in.mu.RLock()
	defer in.mu.RUnlock()
	r, ok := in.results[taskID]
	if !ok {
		return nil
	}
	// Return a copy to avoid data races on the caller side.
	cp := *r
	return &cp
}

// Len returns the number of entries in the inbox (useful for tests).
func (in *Inbox) Len() int {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return len(in.results)
}
