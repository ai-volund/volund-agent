package skill

import (
	"context"
	"testing"
)

func TestMCPClientLifecycle(t *testing.T) {
	bin := mcpEchoBinary(t)
	ctx := context.Background()

	client, err := StartMCPProcess(ctx, bin)
	if err != nil {
		t.Fatalf("StartMCPProcess error: %v", err)
	}
	defer client.Stop()

	// List tools.
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Call echo.
	result, isError, err := client.CallTool(ctx, "echo", map[string]any{"message": "test123"})
	if err != nil {
		t.Fatalf("CallTool echo error: %v", err)
	}
	if isError {
		t.Errorf("echo returned isError=true")
	}
	if result != "test123" {
		t.Errorf("echo expected 'test123', got %q", result)
	}

	// Call uppercase.
	result, isError, err = client.CallTool(ctx, "uppercase", map[string]any{"text": "hello world"})
	if err != nil {
		t.Fatalf("CallTool uppercase error: %v", err)
	}
	if isError {
		t.Errorf("uppercase returned isError=true")
	}
	if result != "HELLO WORLD" {
		t.Errorf("uppercase expected 'HELLO WORLD', got %q", result)
	}

	// Call unknown tool.
	_, isError, err = client.CallTool(ctx, "nonexistent", map[string]any{})
	if err != nil {
		// MCP server returns isError=true in the result, not a transport error.
		// But our mcpTool wrapper converts isError to an error. Direct client should
		// return the result without wrapping.
	}
	if !isError {
		t.Error("calling unknown tool should return isError=true")
	}
}

func TestMCPClientMultipleCalls(t *testing.T) {
	bin := mcpEchoBinary(t)
	ctx := context.Background()

	client, err := StartMCPProcess(ctx, bin)
	if err != nil {
		t.Fatalf("StartMCPProcess error: %v", err)
	}
	defer client.Stop()

	// Make several sequential calls to verify request/response matching.
	for i := 0; i < 10; i++ {
		msg := "msg-" + string(rune('A'+i))
		result, _, err := client.CallTool(ctx, "echo", map[string]any{"message": msg})
		if err != nil {
			t.Fatalf("call %d error: %v", i, err)
		}
		if result != msg {
			t.Errorf("call %d: expected %q, got %q", i, msg, result)
		}
	}
}
