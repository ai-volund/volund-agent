package builtin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// DispatchFunc is called by CreateTask to publish a sub-task to a specialist
// agent pool via NATS. It returns the generated task ID or an error.
// The runtime wires this to stream.PublishTask + inbox bookkeeping.
type DispatchFunc func(ctx context.Context, profileName, taskDescription string, extra map[string]any) (taskID string, err error)

// TaskResultEntry is returned by TaskInbox.Get to report the current state of
// a delegated task. Implemented by *runtime.TaskResult.
type TaskResultEntry struct {
	TaskID  string
	Status  string // "pending", "complete", "error"
	Content string
	Error   string
}

// TaskInbox is the interface used by GetTaskResult to look up specialist results.
// Implemented by *runtime.Inbox.
type TaskInbox interface {
	Get(taskID string) *TaskResultEntry
}

// CreateTask is an orchestrator-only tool that delegates work to a specialist agent.
// It dispatches a sub-task to a specialist pool via a DispatchFunc callback and
// records the task as pending in the orchestrator's Inbox.
type CreateTask struct {
	dispatch DispatchFunc
}

// GetTaskResult is an orchestrator-only tool that retrieves the result of a
// previously created task from the orchestrator's in-memory Inbox.
type GetTaskResult struct {
	inbox TaskInbox
}

// NewCreateTask creates a CreateTask tool. If dispatch is nil, Execute will
// return an error indicating that dispatch is not configured (useful for tests
// that only validate input parsing).
func NewCreateTask(dispatch DispatchFunc) *CreateTask {
	return &CreateTask{dispatch: dispatch}
}

// NewGetTaskResult creates a GetTaskResult tool backed by the given TaskInbox.
// If inbox is nil, Execute will return a "not configured" error.
func NewGetTaskResult(inbox TaskInbox) *GetTaskResult {
	return &GetTaskResult{inbox: inbox}
}

// --- CreateTask ---

type createTaskInput struct {
	SpecialistProfileID string         `json:"specialist_profile_id"`
	TaskDescription     string         `json:"task_description"`
	Context             map[string]any `json:"context,omitempty"`
}

func (CreateTask) Name() string { return "create_task" }

func (CreateTask) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:        "create_task",
		Description: "Delegate a task to a specialist agent. Returns a task_id to use with get_task_result once the specialist completes. Use this to parallelize work or invoke domain-specific capabilities.",
		InputSchemaJson: `{
			"type": "object",
			"required": ["specialist_profile_id", "task_description"],
			"properties": {
				"specialist_profile_id": {
					"type": "string",
					"description": "The AgentProfile ID of the specialist to invoke (e.g. 'code-specialist', 'research-specialist')"
				},
				"task_description": {
					"type": "string",
					"description": "Full description of the task for the specialist, including all necessary context"
				},
				"context": {
					"type": "object",
					"description": "Optional structured context to pass alongside the task description",
					"additionalProperties": true
				}
			}
		}`,
	}
}

func (c *CreateTask) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input createTaskInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid create_task input: %w", err)
	}
	if input.SpecialistProfileID == "" {
		return "", fmt.Errorf("specialist_profile_id is required")
	}
	if input.TaskDescription == "" {
		return "", fmt.Errorf("task_description is required")
	}

	if c.dispatch == nil {
		return "", fmt.Errorf("task dispatch not configured")
	}

	taskID, err := c.dispatch(ctx, input.SpecialistProfileID, input.TaskDescription, input.Context)
	if err != nil {
		return "", fmt.Errorf("dispatching task to %s: %w", input.SpecialistProfileID, err)
	}

	return fmt.Sprintf(`{"task_id":%q,"status":"dispatched","specialist_profile_id":%q}`,
		taskID, input.SpecialistProfileID), nil
}

// --- GetTaskResult ---

type getTaskResultInput struct {
	TaskID string `json:"task_id"`
}

func (GetTaskResult) Name() string { return "get_task_result" }

func (GetTaskResult) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:        "get_task_result",
		Description: "Retrieve the result of a previously created task. Returns the specialist's output if complete, or a 'pending' status if still running.",
		InputSchemaJson: `{
			"type": "object",
			"required": ["task_id"],
			"properties": {
				"task_id": {
					"type": "string",
					"description": "The task_id returned by create_task"
				}
			}
		}`,
	}
}

func (g *GetTaskResult) Execute(_ context.Context, inputJSON string) (string, error) {
	var input getTaskResultInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid get_task_result input: %w", err)
	}
	if input.TaskID == "" {
		return "", fmt.Errorf("task_id is required")
	}

	if g.inbox == nil {
		return "", fmt.Errorf("task inbox not configured")
	}

	result := g.inbox.Get(input.TaskID)
	if result == nil {
		return fmt.Sprintf(`{"task_id":%q,"status":"unknown","message":"No task with this ID found in the inbox."}`,
			input.TaskID), nil
	}

	switch result.Status {
	case "complete":
		return fmt.Sprintf(`{"task_id":%q,"status":"complete","content":%s}`,
			input.TaskID, mustJSON(result.Content)), nil
	case "error":
		return fmt.Sprintf(`{"task_id":%q,"status":"error","error":%s}`,
			input.TaskID, mustJSON(result.Error)), nil
	default:
		return fmt.Sprintf(`{"task_id":%q,"status":"pending"}`, input.TaskID), nil
	}
}

// newTaskID generates a random task ID.
func newTaskID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("task-%x", b)
}

// mustJSON marshals s as a JSON string. Falls back to quoting on error.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return string(b)
}
