package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ai-volund/volund-agent/internal/safety"
)

// Memory represents a stored memory entry.
type Memory struct {
	// ID is the unique identifier for this memory.
	ID string
	// Content is the textual content of the memory.
	Content string
	// Type categorizes the memory (e.g. "observation", "reflection", "plan").
	Type string
	// Embedding is the vector embedding for similarity search.
	Embedding []float64
	// Importance is a score indicating memory relevance (0.0 to 1.0).
	Importance float64
	// CreatedAt is when the memory was stored.
	CreatedAt time.Time
}

// Manager defines the interface for agent memory storage and retrieval.
type Manager interface {
	// SetConversation scopes subsequent session operations to a conversation.
	// Must be called at the start of each task before any session read/write.
	SetConversation(convID string)
	// StoreSession stores a key-value pair in short-term session memory.
	StoreSession(ctx context.Context, key, value string) error
	// GetSession retrieves a value from session memory by key.
	GetSession(ctx context.Context, key string) (string, error)
	// AppendMessage appends a message to the conversation's session history.
	// role is "user" or "assistant", content is the text.
	AppendMessage(ctx context.Context, role, content string) error
	// GetHistory returns the last N messages from session history as a
	// formatted string suitable for prompt injection.
	GetHistory(ctx context.Context, limit int) (string, error)
	// StoreLongTerm persists a memory entry to long-term storage.
	StoreLongTerm(ctx context.Context, mem Memory) error
	// SearchSimilar finds memories similar to the query using vector search.
	SearchSimilar(ctx context.Context, query string, limit int) ([]Memory, error)
	// RetrieveContext searches for memories relevant to the query and returns
	// them formatted as a block suitable for system prompt injection.
	// Returns empty string if no relevant memories are found.
	RetrieveContext(ctx context.Context, query string, limit int) string
}

// noopManager is a no-op implementation of Manager for development use.
type noopManager struct{}

// NewNoopManager returns a Manager that performs no actual storage. Useful
// during development when no memory backend is available.
func NewNoopManager() Manager {
	return &noopManager{}
}

func (n *noopManager) StoreSession(_ context.Context, _, _ string) error {
	return nil
}

func (n *noopManager) GetSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (n *noopManager) StoreLongTerm(_ context.Context, _ Memory) error {
	return nil
}

func (n *noopManager) SearchSimilar(_ context.Context, _ string, _ int) ([]Memory, error) {
	return nil, nil
}

func (n *noopManager) SetConversation(_ string) {}

func (n *noopManager) AppendMessage(_ context.Context, _, _ string) error {
	return nil
}

func (n *noopManager) GetHistory(_ context.Context, _ int) (string, error) {
	return "", nil
}

func (n *noopManager) RetrieveContext(_ context.Context, _ string, _ int) string {
	return ""
}

// FormatMemories formats a slice of memories into a block for system prompt injection.
// All memory content is sanitized to mitigate prompt injection attacks.
func FormatMemories(memories []Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(safety.WrapExternal("memories", formatMemoriesInner(memories)))
	return b.String()
}

func formatMemoriesInner(memories []Memory) string {
	var b strings.Builder
	for _, m := range memories {
		fmt.Fprintf(&b, "[%s] %s\n", m.Type, safety.SanitizeMemory(m.Content))
	}
	return b.String()
}
