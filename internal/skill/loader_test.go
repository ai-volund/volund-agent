package skill

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mcpEchoBinary(t *testing.T) string {
	t.Helper()
	// Walk up to find the repo root (contains go.mod).
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("cannot find repo root")
		}
		dir = parent
	}
	bin := filepath.Join(dir, "bin", "mcp-echo")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("mcp-echo binary not found at %s, run: go build -o bin/mcp-echo ./cmd/mcp-echo", bin)
	}
	return bin
}

func TestLoadPromptSkills(t *testing.T) {
	ctx := context.Background()
	skills := []Spec{
		{
			Name:        "code-review",
			Type:        "prompt",
			Version:     "1.0.0",
			Description: "Code review guidelines",
			Prompt:      "Review code for security and quality.",
		},
		{
			Name:        "sql-helper",
			Type:        "prompt",
			Version:     "1.0.0",
			Description: "SQL query helper",
			Prompt:      "Help write efficient SQL queries.",
		},
	}

	result, err := Load(ctx, skills)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools from prompt skills, got %d", len(result.Tools))
	}

	if !strings.Contains(result.PromptExtensions, "code-review") {
		t.Error("prompt extensions should contain code-review skill name")
	}
	if !strings.Contains(result.PromptExtensions, "Review code for security") {
		t.Error("prompt extensions should contain code-review prompt content")
	}
	if !strings.Contains(result.PromptExtensions, "sql-helper") {
		t.Error("prompt extensions should contain sql-helper skill name")
	}
}

func TestLoadPromptSkillEmpty(t *testing.T) {
	ctx := context.Background()

	result, err := Load(ctx, nil)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if result.PromptExtensions != "" {
		t.Errorf("expected empty prompt extensions for nil skills, got %q", result.PromptExtensions)
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
}

func TestLoadMCPSkill(t *testing.T) {
	bin := mcpEchoBinary(t)
	ctx := context.Background()

	skills := []Spec{
		{
			Name:        "echo",
			Type:        "mcp",
			Version:     "1.0.0",
			Description: "Test echo MCP server",
			Runtime: &RuntimeSpec{
				Image:     bin, // Use the binary path as the "image" for stdio transport.
				Transport: "stdio",
			},
		},
	}

	result, err := Load(ctx, skills)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// mcp-echo exposes 2 tools: echo and uppercase.
	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 MCP tools, got %d", len(result.Tools))
	}

	// Verify tool names.
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name()] = true
	}
	if !names["echo"] {
		t.Error("expected 'echo' tool")
	}
	if !names["uppercase"] {
		t.Error("expected 'uppercase' tool")
	}

	// Test echo tool execution.
	for _, tool := range result.Tools {
		if tool.Name() == "echo" {
			out, err := tool.Execute(ctx, `{"message":"hello volund"}`)
			if err != nil {
				t.Fatalf("echo Execute error: %v", err)
			}
			if out != "hello volund" {
				t.Errorf("echo expected 'hello volund', got %q", out)
			}
		}
		if tool.Name() == "uppercase" {
			out, err := tool.Execute(ctx, `{"text":"hello"}`)
			if err != nil {
				t.Fatalf("uppercase Execute error: %v", err)
			}
			if out != "HELLO" {
				t.Errorf("uppercase expected 'HELLO', got %q", out)
			}
		}
	}

	// Verify definitions have schemas.
	for _, tool := range result.Tools {
		def := tool.Definition()
		if def.Name == "" {
			t.Error("tool definition missing name")
		}
		if def.InputSchemaJson == "" {
			t.Errorf("tool %s definition missing input schema", def.Name)
		}
	}

	// Cleanup.
	if len(result.Clients) != 1 {
		t.Fatalf("expected 1 MCP client, got %d", len(result.Clients))
	}
	result.Clients[0].Stop()
}

func TestLoadMixedSkills(t *testing.T) {
	bin := mcpEchoBinary(t)
	ctx := context.Background()

	skills := []Spec{
		{
			Name:        "guidelines",
			Type:        "prompt",
			Version:     "1.0.0",
			Description: "General guidelines",
			Prompt:      "Always be helpful.",
		},
		{
			Name:        "echo-server",
			Type:        "mcp",
			Version:     "1.0.0",
			Description: "Test MCP",
			Runtime: &RuntimeSpec{
				Image:     bin,
				Transport: "stdio",
			},
		},
	}

	result, err := Load(ctx, skills)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// 1 prompt skill → prompt extensions.
	if !strings.Contains(result.PromptExtensions, "guidelines") {
		t.Error("missing prompt extension for guidelines skill")
	}

	// 2 MCP tools from echo server.
	if len(result.Tools) != 2 {
		t.Errorf("expected 2 MCP tools, got %d", len(result.Tools))
	}

	for _, c := range result.Clients {
		c.Stop()
	}
}

func TestSharedSkillURL(t *testing.T) {
	tests := []struct {
		name     string
		spec     Spec
		expected string
	}{
		{
			name: "default URL from skill name",
			spec: Spec{
				Name:    "github",
				Runtime: &RuntimeSpec{Mode: "shared"},
			},
			expected: "http://skill-github:8080",
		},
		{
			name: "explicit HTTP URL override",
			spec: Spec{
				Name:    "github",
				Runtime: &RuntimeSpec{Mode: "shared", Image: "http://custom-host:9090"},
			},
			expected: "http://custom-host:9090",
		},
		{
			name: "explicit HTTPS URL override",
			spec: Spec{
				Name:    "slack",
				Runtime: &RuntimeSpec{Mode: "shared", Image: "https://skill.example.com"},
			},
			expected: "https://skill.example.com",
		},
		{
			name: "non-URL image uses default convention",
			spec: Spec{
				Name:    "email",
				Runtime: &RuntimeSpec{Mode: "shared", Image: "ghcr.io/ai-volund/skill-email:1.0"},
			},
			expected: "http://skill-email:8080",
		},
		{
			name: "nil runtime uses default",
			spec: Spec{
				Name: "test",
			},
			expected: "http://skill-test:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sharedSkillURL(tt.spec)
			if got != tt.expected {
				t.Errorf("sharedSkillURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestLoadUnknownSkillType(t *testing.T) {
	ctx := context.Background()
	skills := []Spec{
		{
			Name:    "mystery",
			Type:    "unknown",
			Version: "1.0.0",
		},
	}

	result, err := Load(ctx, skills)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// Unknown types are logged and skipped, not errors.
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools for unknown type, got %d", len(result.Tools))
	}
}
