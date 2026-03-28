package skill

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCLIAdapterLifecycle(t *testing.T) {
	// Build the adapter binary.
	buildCmd := exec.Command("go", "build", "-o", t.TempDir()+"/mcp-cli-adapter", "./cmd/mcp-cli-adapter")
	buildCmd.Dir = "../.."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mcp-cli-adapter: %v\n%s", err, out)
	}
	adapterPath := t.TempDir() + "/mcp-cli-adapter"
	// TempDir returns a new dir each call, so use the first one.
	buildCmd2 := exec.Command("go", "build", "-o", "/tmp/test-mcp-cli-adapter", "./cmd/mcp-cli-adapter")
	buildCmd2.Dir = "../.."
	if out, err := buildCmd2.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mcp-cli-adapter: %v\n%s", err, out)
	}
	adapterPath = "/tmp/test-mcp-cli-adapter"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start the adapter wrapping /bin/echo with one allowed command.
	client, err := StartMCPProcess(ctx, adapterPath,
		"--binary=echo",
		"--commands=hello,world",
	)
	if err != nil {
		t.Fatalf("start CLI adapter: %v", err)
	}
	defer client.Stop()

	// List tools — should have echo_hello and echo_world.
	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	// Check tool names.
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["echo_hello"] {
		t.Error("missing tool echo_hello")
	}
	if !names["echo_world"] {
		t.Error("missing tool echo_world")
	}

	// Call echo_hello with extra args — should run: echo hello extra_arg
	result, isError, err := client.CallTool(ctx, "echo_hello", map[string]any{
		"args": "from volund",
	})
	if err != nil {
		t.Fatalf("CallTool echo_hello: %v", err)
	}
	if isError {
		t.Fatalf("echo_hello returned error: %s", result)
	}
	// echo prints: "hello from volund\n"
	expected := "hello from volund\n"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}

	// Call echo_world with no extra args — should run: echo world
	result2, isError2, err := client.CallTool(ctx, "echo_world", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool echo_world: %v", err)
	}
	if isError2 {
		t.Fatalf("echo_world returned error: %s", result2)
	}
	if result2 != "world\n" {
		t.Errorf("expected %q, got %q", "world\n", result2)
	}

	// Call unknown tool — should error.
	_, isError3, err := client.CallTool(ctx, "echo_notallowed", map[string]any{})
	if err != nil {
		t.Fatalf("CallTool unknown: %v", err)
	}
	if !isError3 {
		t.Error("expected error for unknown tool")
	}
}

func TestCLIAdapterViaSkillLoader(t *testing.T) {
	// Build the adapter binary to a temp dir with the expected name.
	binDir := t.TempDir()
	buildCmd := exec.Command("go", "build", "-o", binDir+"/mcp-cli-adapter", "./cmd/mcp-cli-adapter")
	buildCmd.Dir = "../.."
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mcp-cli-adapter: %v\n%s", err, out)
	}

	// Put it in PATH.
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Load a CLI skill via the standard loader.
	result, err := Load(ctx, []Spec{
		{
			Name:    "echo-cli",
			Type:    "cli",
			Version: "1.0.0",
			CLI: &CLISpec{
				Binary:          "echo",
				AllowedCommands: []string{"hello", "goodbye"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}

	// Execute one of the tools.
	output, err := result.Tools[0].Execute(ctx, `{"args": "world"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if output != "hello world\n" {
		t.Errorf("expected %q, got %q", "hello world\n", output)
	}

	// Cleanup.
	for _, c := range result.Clients {
		c.Stop()
	}
}
