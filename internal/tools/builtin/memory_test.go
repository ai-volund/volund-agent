package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-volund/volund-agent/internal/memory"
)

// mockMemManager is a configurable mock of memory.Manager for testing.
type mockMemManager struct {
	searchResults []memory.Memory
	searchErr     error
	storedMemory  *memory.Memory
	storeErr      error
}

func (m *mockMemManager) StoreSession(_ context.Context, _, _ string) error { return nil }
func (m *mockMemManager) GetSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockMemManager) StoreLongTerm(_ context.Context, mem memory.Memory) error {
	m.storedMemory = &mem
	return m.storeErr
}

func (m *mockMemManager) SearchSimilar(_ context.Context, _ string, _ int) ([]memory.Memory, error) {
	return m.searchResults, m.searchErr
}

func (m *mockMemManager) RetrieveContext(_ context.Context, _ string, _ int) string {
	return ""
}

// --- ReadMemory tests ---

func TestReadMemory_Definition(t *testing.T) {
	rm := NewReadMemory(&mockMemManager{})
	def := rm.Definition()

	if def.Name != "read_memory" {
		t.Fatalf("expected name 'read_memory', got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if !strings.Contains(def.InputSchemaJson, "query") {
		t.Fatal("expected schema to contain 'query'")
	}
}

func TestReadMemory_Execute_NoResults(t *testing.T) {
	mock := &mockMemManager{searchResults: nil}
	rm := NewReadMemory(mock)

	out, err := rm.Execute(context.Background(), `{"query":"something"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "No relevant memories found." {
		t.Fatalf("expected 'No relevant memories found.', got %q", out)
	}
}

func TestReadMemory_Execute_WithResults(t *testing.T) {
	mock := &mockMemManager{
		searchResults: []memory.Memory{
			{Type: "fact", Content: "The sky is blue"},
			{Type: "preference", Content: "User prefers Go"},
		},
	}
	rm := NewReadMemory(mock)

	out, err := rm.Execute(context.Background(), `{"query":"colors","limit":5}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "1. [fact] The sky is blue") {
		t.Fatalf("expected formatted first result, got %q", out)
	}
	if !strings.Contains(out, "2. [preference] User prefers Go") {
		t.Fatalf("expected formatted second result, got %q", out)
	}
}

func TestReadMemory_Execute_InvalidJSON(t *testing.T) {
	rm := NewReadMemory(&mockMemManager{})
	_, err := rm.Execute(context.Background(), `{bad}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// --- WriteMemory tests ---

func TestWriteMemory_Definition(t *testing.T) {
	wm := NewWriteMemory(&mockMemManager{})
	def := wm.Definition()

	if def.Name != "write_memory" {
		t.Fatalf("expected name 'write_memory', got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if !strings.Contains(def.InputSchemaJson, "content") {
		t.Fatal("expected schema to contain 'content'")
	}
}

func TestWriteMemory_Execute_Success(t *testing.T) {
	mock := &mockMemManager{}
	wm := NewWriteMemory(mock)

	out, err := wm.Execute(context.Background(), `{"content":"User likes pizza","type":"preference","importance":0.8}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "Memory stored successfully." {
		t.Fatalf("expected success message, got %q", out)
	}
	if mock.storedMemory == nil {
		t.Fatal("expected memory to be stored")
	}
	if mock.storedMemory.Content != "User likes pizza" {
		t.Fatalf("expected content 'User likes pizza', got %q", mock.storedMemory.Content)
	}
	if mock.storedMemory.Type != "preference" {
		t.Fatalf("expected type 'preference', got %q", mock.storedMemory.Type)
	}
	if mock.storedMemory.Importance != 0.8 {
		t.Fatalf("expected importance 0.8, got %f", mock.storedMemory.Importance)
	}
}

func TestWriteMemory_Execute_Defaults(t *testing.T) {
	mock := &mockMemManager{}
	wm := NewWriteMemory(mock)

	out, err := wm.Execute(context.Background(), `{"content":"Some fact"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "Memory stored successfully." {
		t.Fatalf("expected success message, got %q", out)
	}
	if mock.storedMemory == nil {
		t.Fatal("expected memory to be stored")
	}
	if mock.storedMemory.Type != "fact" {
		t.Fatalf("expected default type 'fact', got %q", mock.storedMemory.Type)
	}
	if mock.storedMemory.Importance != 0.5 {
		t.Fatalf("expected default importance 0.5, got %f", mock.storedMemory.Importance)
	}
}

func TestWriteMemory_Execute_InvalidJSON(t *testing.T) {
	wm := NewWriteMemory(&mockMemManager{})
	_, err := wm.Execute(context.Background(), `{nope}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
