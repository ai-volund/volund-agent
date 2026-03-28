package tools

import (
	"context"
	"errors"
	"testing"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// fakeTool is a minimal Tool implementation for testing the registry.
type fakeTool struct {
	name    string
	output  string
	err     error
	schema  string
}

func (f *fakeTool) Name() string { return f.name }

func (f *fakeTool) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:            f.name,
		Description:     "fake tool for testing",
		InputSchemaJson: f.schema,
	}
}

func (f *fakeTool) Execute(_ context.Context, _ string) (string, error) {
	return f.output, f.err
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()
	tool := &fakeTool{name: "test_tool", output: "ok"}
	r.Register(tool)

	if !r.Has("test_tool") {
		t.Fatal("expected registry to contain test_tool after registration")
	}
	if r.Has("nonexistent") {
		t.Fatal("expected registry to not contain nonexistent tool")
	}
}

func TestRegistry_RegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	tool := &fakeTool{name: "dup_tool"}
	r.Register(tool)

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on duplicate registration")
		}
		msg, ok := rec.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", rec, rec)
		}
		if msg == "" {
			t.Fatal("expected non-empty panic message")
		}
	}()

	r.Register(&fakeTool{name: "dup_tool"})
}

func TestRegistry_Execute_Unknown(t *testing.T) {
	r := NewRegistry()
	result := r.Execute(context.Background(), Call{
		ID:   "call-1",
		Name: "nonexistent_tool",
	})

	if !result.IsError {
		t.Fatal("expected error result for unknown tool")
	}
	if result.CallID != "call-1" {
		t.Fatalf("expected CallID call-1, got %s", result.CallID)
	}
	if result.Content == "" {
		t.Fatal("expected non-empty error content")
	}
}

func TestRegistry_Execute_Success(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "echo", output: "hello world"})

	result := r.Execute(context.Background(), Call{
		ID:        "call-2",
		Name:      "echo",
		InputJSON: `{}`,
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "hello world" {
		t.Fatalf("expected 'hello world', got %q", result.Content)
	}
	if result.CallID != "call-2" {
		t.Fatalf("expected CallID call-2, got %s", result.CallID)
	}
}

func TestRegistry_Execute_ToolError(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "fail", output: "partial output", err: errors.New("boom")})

	result := r.Execute(context.Background(), Call{
		ID:   "call-err",
		Name: "fail",
	})

	if !result.IsError {
		t.Fatal("expected error result when tool returns error")
	}
	if result.Content != "boom" {
		t.Fatalf("expected error content 'boom', got %q", result.Content)
	}
}

func TestRegistry_BeforeHook_Block(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "blocked_tool", output: "should not see this"})

	r.AddBeforeHook(func(_ context.Context, call Call) (bool, string, error) {
		if call.Name == "blocked_tool" {
			return true, "policy violation", nil
		}
		return false, "", nil
	})

	result := r.Execute(context.Background(), Call{
		ID:   "call-3",
		Name: "blocked_tool",
	})

	if !result.IsError {
		t.Fatal("expected error result when before hook blocks")
	}
	if result.Content != "blocked: policy violation" {
		t.Fatalf("expected 'blocked: policy violation', got %q", result.Content)
	}
}

func TestRegistry_BeforeHook_Error(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "some_tool", output: "ok"})

	r.AddBeforeHook(func(_ context.Context, _ Call) (bool, string, error) {
		return false, "", errors.New("hook exploded")
	})

	result := r.Execute(context.Background(), Call{
		ID:   "call-he",
		Name: "some_tool",
	})

	if !result.IsError {
		t.Fatal("expected error result when before hook returns error")
	}
	if result.Content != "hook error: hook exploded" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestRegistry_AfterHook_Transform(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "transform_tool", output: "original"})

	r.AddAfterHook(func(_ context.Context, _ Call, res Result) (Result, error) {
		res.Content = "transformed: " + res.Content
		return res, nil
	})

	result := r.Execute(context.Background(), Call{
		ID:   "call-4",
		Name: "transform_tool",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "transformed: original" {
		t.Fatalf("expected 'transformed: original', got %q", result.Content)
	}
}

func TestRegistry_AfterHook_ErrorIgnored(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "after_err_tool", output: "keep this"})

	r.AddAfterHook(func(_ context.Context, _ Call, _ Result) (Result, error) {
		return Result{}, errors.New("after hook failed")
	})

	result := r.Execute(context.Background(), Call{
		ID:   "call-ae",
		Name: "after_err_tool",
	})

	// After hook errors are logged but don't clobber the result
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "keep this" {
		t.Fatalf("expected 'keep this', got %q", result.Content)
	}
}

func TestRegistry_Definitions(t *testing.T) {
	r := NewRegistry()
	r.Register(&fakeTool{name: "a", schema: `{"type":"object"}`})
	r.Register(&fakeTool{name: "b", schema: `{"type":"object"}`})

	defs := r.Definitions()
	if len(defs) != 2 {
		t.Fatalf("expected 2 definitions, got %d", len(defs))
	}

	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["a"] || !names["b"] {
		t.Fatalf("expected definitions for 'a' and 'b', got %v", names)
	}
}
