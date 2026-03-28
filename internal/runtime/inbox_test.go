package runtime

import (
	"sync"
	"testing"
)

func TestInbox_SetPendingAndGet(t *testing.T) {
	inbox := NewInbox()
	inbox.SetPending("task-001")

	r := inbox.Get("task-001")
	if r == nil {
		t.Fatal("expected result, got nil")
	}
	if r.Status != "pending" {
		t.Fatalf("expected status pending, got %q", r.Status)
	}
	if r.TaskID != "task-001" {
		t.Fatalf("expected task_id task-001, got %q", r.TaskID)
	}
	if r.UpdatedAt.IsZero() {
		t.Fatal("expected non-zero UpdatedAt")
	}
}

func TestInbox_GetUnknown(t *testing.T) {
	inbox := NewInbox()
	r := inbox.Get("nonexistent")
	if r != nil {
		t.Fatalf("expected nil for unknown task, got %+v", r)
	}
}

func TestInbox_PutOverwritesPending(t *testing.T) {
	inbox := NewInbox()
	inbox.SetPending("task-002")

	inbox.Put(&TaskResult{
		TaskID:  "task-002",
		Status:  "complete",
		Content: "specialist result",
	})

	r := inbox.Get("task-002")
	if r == nil {
		t.Fatal("expected result, got nil")
	}
	if r.Status != "complete" {
		t.Fatalf("expected status complete, got %q", r.Status)
	}
	if r.Content != "specialist result" {
		t.Fatalf("expected content 'specialist result', got %q", r.Content)
	}
}

func TestInbox_PutError(t *testing.T) {
	inbox := NewInbox()
	inbox.Put(&TaskResult{
		TaskID: "task-003",
		Status: "error",
		Error:  "specialist crashed",
	})

	r := inbox.Get("task-003")
	if r == nil {
		t.Fatal("expected result, got nil")
	}
	if r.Status != "error" {
		t.Fatalf("expected status error, got %q", r.Status)
	}
	if r.Error != "specialist crashed" {
		t.Fatalf("expected error 'specialist crashed', got %q", r.Error)
	}
}

func TestInbox_Len(t *testing.T) {
	inbox := NewInbox()
	if inbox.Len() != 0 {
		t.Fatalf("expected 0, got %d", inbox.Len())
	}
	inbox.SetPending("a")
	inbox.SetPending("b")
	if inbox.Len() != 2 {
		t.Fatalf("expected 2, got %d", inbox.Len())
	}
}

func TestInbox_GetReturnsCopy(t *testing.T) {
	inbox := NewInbox()
	inbox.Put(&TaskResult{TaskID: "task-copy", Status: "complete", Content: "original"})

	r := inbox.Get("task-copy")
	r.Content = "modified"

	r2 := inbox.Get("task-copy")
	if r2.Content != "original" {
		t.Fatalf("Get should return a copy; inbox was modified to %q", r2.Content)
	}
}

func TestInbox_ConcurrentAccess(t *testing.T) {
	inbox := NewInbox()
	var wg sync.WaitGroup

	// Concurrent writers.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			taskID := "task-concurrent"
			if id%2 == 0 {
				inbox.SetPending(taskID)
			} else {
				inbox.Put(&TaskResult{TaskID: taskID, Status: "complete", Content: "done"})
			}
		}(i)
	}

	// Concurrent readers.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = inbox.Get("task-concurrent")
		}()
	}

	wg.Wait()

	// Should not panic or race-detect. Final state is either pending or complete.
	r := inbox.Get("task-concurrent")
	if r == nil {
		t.Fatal("expected result after concurrent writes")
	}
	if r.Status != "pending" && r.Status != "complete" {
		t.Fatalf("unexpected status %q", r.Status)
	}
}
