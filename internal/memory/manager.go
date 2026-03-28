package memory

import (
	"context"
	"time"
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
	// StoreSession stores a key-value pair in short-term session memory.
	StoreSession(ctx context.Context, key, value string) error
	// GetSession retrieves a value from session memory by key.
	GetSession(ctx context.Context, key string) (string, error)
	// StoreLongTerm persists a memory entry to long-term storage.
	StoreLongTerm(ctx context.Context, mem Memory) error
	// SearchSimilar finds memories similar to the query using vector search.
	SearchSimilar(ctx context.Context, query string, limit int) ([]Memory, error)
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
