package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"

	"github.com/ai-volund/volund-agent/internal/memory"
)

// ReadMemory searches long-term memory by semantic similarity.
type ReadMemory struct {
	mem memory.Manager
}

// WriteMemory stores a fact, preference, or insight to long-term memory.
type WriteMemory struct {
	mem memory.Manager
}

// NewReadMemory creates a ReadMemory tool backed by the given memory manager.
func NewReadMemory(mem memory.Manager) *ReadMemory {
	return &ReadMemory{mem: mem}
}

// NewWriteMemory creates a WriteMemory tool backed by the given memory manager.
func NewWriteMemory(mem memory.Manager) *WriteMemory {
	return &WriteMemory{mem: mem}
}

// --- ReadMemory ---

type readMemInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func (ReadMemory) Name() string { return "read_memory" }

func (ReadMemory) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:        "read_memory",
		Description: "Search long-term memory for information relevant to the query. Returns stored facts, preferences, and learned context.",
		InputSchemaJson: `{
			"type": "object",
			"required": ["query"],
			"properties": {
				"query": {
					"type": "string",
					"description": "What to search for in memory"
				},
				"limit": {
					"type": "integer",
					"description": "Max number of results to return (default 5)",
					"minimum": 1,
					"maximum": 20
				}
			}
		}`,
	}
}

func (r *ReadMemory) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input readMemInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid read_memory input: %w", err)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	mems, err := r.mem.SearchSimilar(ctx, input.Query, limit)
	if err != nil {
		return "", fmt.Errorf("memory search: %w", err)
	}
	if len(mems) == 0 {
		return "No relevant memories found.", nil
	}
	var sb strings.Builder
	for i, m := range mems {
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, m.Type, m.Content)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// --- WriteMemory ---

type writeMemInput struct {
	Content    string  `json:"content"`
	Type       string  `json:"type,omitempty"`
	Importance float64 `json:"importance,omitempty"`
}

func (WriteMemory) Name() string { return "write_memory" }

func (WriteMemory) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:        "write_memory",
		Description: "Store an important fact, user preference, or learned insight to long-term memory for future conversations.",
		InputSchemaJson: `{
			"type": "object",
			"required": ["content"],
			"properties": {
				"content": {
					"type": "string",
					"description": "The information to store in memory"
				},
				"type": {
					"type": "string",
					"enum": ["fact", "preference", "learned", "entity"],
					"description": "Memory type (default: fact)"
				},
				"importance": {
					"type": "number",
					"description": "Importance score 0.0-1.0 (default 0.5)",
					"minimum": 0.0,
					"maximum": 1.0
				}
			}
		}`,
	}
}

func (w *WriteMemory) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input writeMemInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid write_memory input: %w", err)
	}
	memType := input.Type
	if memType == "" {
		memType = "fact"
	}
	importance := input.Importance
	if importance <= 0 {
		importance = 0.5
	}
	if err := w.mem.StoreLongTerm(ctx, memory.Memory{
		Content:    input.Content,
		Type:       memType,
		Importance: importance,
	}); err != nil {
		return "", fmt.Errorf("storing memory: %w", err)
	}
	return "Memory stored successfully.", nil
}
