package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// fakeDispatch returns a DispatchFunc that records calls and returns a fixed task ID.
func fakeDispatch(taskID string) (DispatchFunc, *[]string) {
	var calls []string
	fn := func(_ context.Context, profileName, taskDescription string, extra map[string]any) (string, error) {
		calls = append(calls, profileName+":"+taskDescription)
		return taskID, nil
	}
	return fn, &calls
}

// failingDispatch returns a DispatchFunc that always errors.
func failingDispatch(errMsg string) DispatchFunc {
	return func(_ context.Context, _, _ string, _ map[string]any) (string, error) {
		return "", fmt.Errorf("%s", errMsg)
	}
}

// fakeInbox implements TaskInbox for testing.
type fakeInbox struct {
	results map[string]*TaskResultEntry
}

func newFakeInbox() *fakeInbox {
	return &fakeInbox{results: make(map[string]*TaskResultEntry)}
}

func (f *fakeInbox) Get(taskID string) *TaskResultEntry {
	return f.results[taskID]
}

func (f *fakeInbox) put(entry *TaskResultEntry) {
	f.results[entry.TaskID] = entry
}

func TestCreateTask_Definition(t *testing.T) {
	ct := NewCreateTask(nil)
	def := ct.Definition()

	if def.Name != "create_task" {
		t.Fatalf("expected name 'create_task', got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if !strings.Contains(def.InputSchemaJson, "specialist_profile_id") {
		t.Fatal("expected schema to contain 'specialist_profile_id'")
	}
	if !strings.Contains(def.InputSchemaJson, "task_description") {
		t.Fatal("expected schema to contain 'task_description'")
	}
}

func TestCreateTask_Execute_Dispatches(t *testing.T) {
	dispatch, calls := fakeDispatch("task-abc123")
	ct := NewCreateTask(dispatch)

	input := `{"specialist_profile_id":"code-specialist","task_description":"Write a function"}`
	out, err := ct.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify dispatch was called.
	if len(*calls) != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", len(*calls))
	}
	if (*calls)[0] != "code-specialist:Write a function" {
		t.Fatalf("unexpected dispatch call: %q", (*calls)[0])
	}

	// Parse the JSON output.
	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}

	if result["status"] != "dispatched" {
		t.Fatalf("expected status 'dispatched', got %q", result["status"])
	}
	if result["specialist_profile_id"] != "code-specialist" {
		t.Fatalf("expected specialist_profile_id 'code-specialist', got %q", result["specialist_profile_id"])
	}
	if result["task_id"] != "task-abc123" {
		t.Fatalf("expected task_id 'task-abc123', got %q", result["task_id"])
	}
}

func TestCreateTask_Execute_DispatchError(t *testing.T) {
	ct := NewCreateTask(failingDispatch("NATS connection refused"))

	input := `{"specialist_profile_id":"code-specialist","task_description":"Write a function"}`
	_, err := ct.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "NATS connection refused") {
		t.Fatalf("expected dispatch error, got %q", err.Error())
	}
}

func TestCreateTask_Execute_NilDispatch(t *testing.T) {
	ct := NewCreateTask(nil)

	input := `{"specialist_profile_id":"code-specialist","task_description":"Write a function"}`
	_, err := ct.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for nil dispatch")
	}
	if !strings.Contains(err.Error(), "dispatch not configured") {
		t.Fatalf("expected 'dispatch not configured' error, got %q", err.Error())
	}
}

func TestCreateTask_Execute_MissingFields(t *testing.T) {
	dispatch, _ := fakeDispatch("task-xxx")
	ct := NewCreateTask(dispatch)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "missing specialist_profile_id",
			input: `{"task_description":"Do something"}`,
			want:  "specialist_profile_id is required",
		},
		{
			name:  "missing task_description",
			input: `{"specialist_profile_id":"some-specialist"}`,
			want:  "task_description is required",
		},
		{
			name:  "invalid JSON",
			input: `{bad}`,
			want:  "invalid create_task input",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ct.Execute(context.Background(), tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}
}

func TestGetTaskResult_Definition(t *testing.T) {
	gtr := NewGetTaskResult(nil)
	def := gtr.Definition()

	if def.Name != "get_task_result" {
		t.Fatalf("expected name 'get_task_result', got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if !strings.Contains(def.InputSchemaJson, "task_id") {
		t.Fatal("expected schema to contain 'task_id'")
	}
}

func TestGetTaskResult_Execute_Pending(t *testing.T) {
	inbox := newFakeInbox()
	inbox.put(&TaskResultEntry{TaskID: "task-abc123", Status: "pending"})
	gtr := NewGetTaskResult(inbox)

	out, err := gtr.Execute(context.Background(), `{"task_id":"task-abc123"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}

	if result["status"] != "pending" {
		t.Fatalf("expected status 'pending', got %q", result["status"])
	}
	if result["task_id"] != "task-abc123" {
		t.Fatalf("expected task_id 'task-abc123', got %q", result["task_id"])
	}
}

func TestGetTaskResult_Execute_Complete(t *testing.T) {
	inbox := newFakeInbox()
	inbox.put(&TaskResultEntry{
		TaskID:  "task-done",
		Status:  "complete",
		Content: "The answer is 42.",
	})
	gtr := NewGetTaskResult(inbox)

	out, err := gtr.Execute(context.Background(), `{"task_id":"task-done"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}

	if result["status"] != "complete" {
		t.Fatalf("expected status 'complete', got %v", result["status"])
	}
	if result["content"] != "The answer is 42." {
		t.Fatalf("expected content 'The answer is 42.', got %v", result["content"])
	}
}

func TestGetTaskResult_Execute_Error(t *testing.T) {
	inbox := newFakeInbox()
	inbox.put(&TaskResultEntry{
		TaskID: "task-err",
		Status: "error",
		Error:  "specialist timed out",
	})
	gtr := NewGetTaskResult(inbox)

	out, err := gtr.Execute(context.Background(), `{"task_id":"task-err"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}

	if result["status"] != "error" {
		t.Fatalf("expected status 'error', got %v", result["status"])
	}
	if result["error"] != "specialist timed out" {
		t.Fatalf("expected error 'specialist timed out', got %v", result["error"])
	}
}

func TestGetTaskResult_Execute_Unknown(t *testing.T) {
	inbox := newFakeInbox()
	gtr := NewGetTaskResult(inbox)

	out, err := gtr.Execute(context.Background(), `{"task_id":"task-nope"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", out, err)
	}
	if result["status"] != "unknown" {
		t.Fatalf("expected status 'unknown', got %q", result["status"])
	}
}

func TestGetTaskResult_Execute_NilInbox(t *testing.T) {
	gtr := NewGetTaskResult(nil)

	_, err := gtr.Execute(context.Background(), `{"task_id":"task-abc123"}`)
	if err == nil {
		t.Fatal("expected error for nil inbox")
	}
	if !strings.Contains(err.Error(), "inbox not configured") {
		t.Fatalf("expected 'inbox not configured' error, got %q", err.Error())
	}
}

func TestGetTaskResult_Execute_MissingTaskID(t *testing.T) {
	gtr := NewGetTaskResult(newFakeInbox())

	_, err := gtr.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error for missing task_id")
	}
	if !strings.Contains(err.Error(), "task_id is required") {
		t.Fatalf("expected 'task_id is required' error, got %q", err.Error())
	}
}

func TestGetTaskResult_Execute_InvalidJSON(t *testing.T) {
	gtr := NewGetTaskResult(newFakeInbox())

	_, err := gtr.Execute(context.Background(), `{bad}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
