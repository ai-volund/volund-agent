package skill

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// ToolCaller is the interface both MCPClient and MCPHTTPClient satisfy.
type ToolCaller interface {
	ListTools(ctx context.Context) ([]mcpToolDef, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (string, bool, error)
	Stop()
}

// LoadResult contains everything the agent runtime needs from skill resolution.
type LoadResult struct {
	// PromptExtensions is additional text to append to the system prompt
	// (from prompt-type skills).
	PromptExtensions string

	// Tools are MCP/CLI tool wrappers ready for registration in the tool registry.
	Tools []Tool

	// Clients are MCP clients that must be stopped on shutdown.
	Clients []ToolCaller
}

// Tool matches the agent's tools.Tool interface so the loader doesn't
// import the tools package (avoiding circular deps).
type Tool interface {
	Name() string
	Definition() *volundv1.ToolDefinition
	Execute(ctx context.Context, inputJSON string) (string, error)
}

// Load resolves a list of skill specs into prompt extensions and tool
// implementations. For MCP skills where the sidecar is already running
// (started by the operator), it connects via stdio. For HTTP-based MCP
// servers, it connects via HTTP.
func Load(ctx context.Context, skills []Spec) (*LoadResult, error) {
	result := &LoadResult{}
	var promptParts []string

	for _, s := range skills {
		switch s.Type {
		case "prompt":
			if s.Prompt != "" {
				promptParts = append(promptParts, fmt.Sprintf("## Skill: %s\n\n%s", s.Name, s.Prompt))
				slog.Info("loaded prompt skill", "name", s.Name)
			}

		case "mcp":
			client, err := connectMCP(ctx, s)
			if err != nil {
				slog.Warn("failed to connect MCP skill, skipping", "name", s.Name, "error", err)
				continue
			}
			tools, err := discoverTools(ctx, s.Name, client)
			if err != nil {
				slog.Warn("failed to discover MCP tools, skipping", "name", s.Name, "error", err)
				client.Stop()
				continue
			}
			result.Tools = append(result.Tools, tools...)
			result.Clients = append(result.Clients, client)
			slog.Info("loaded MCP skill", "name", s.Name, "tools", len(tools))

		case "cli":
			if s.CLI == nil || s.CLI.Binary == "" {
				slog.Warn("CLI skill missing binary, skipping", "name", s.Name)
				continue
			}
			if len(s.CLI.AllowedCommands) == 0 {
				slog.Warn("CLI skill has no allowed commands, skipping", "name", s.Name)
				continue
			}
			client, err := startCLIAdapter(ctx, s)
			if err != nil {
				slog.Warn("failed to start CLI adapter, skipping", "name", s.Name, "error", err)
				continue
			}
			tools, err := discoverTools(ctx, s.Name, client)
			if err != nil {
				slog.Warn("failed to discover CLI tools, skipping", "name", s.Name, "error", err)
				client.Stop()
				continue
			}
			result.Tools = append(result.Tools, tools...)
			result.Clients = append(result.Clients, client)
			slog.Info("loaded CLI skill", "name", s.Name, "binary", s.CLI.Binary, "tools", len(tools))

		default:
			slog.Warn("unknown skill type, skipping", "name", s.Name, "type", s.Type)
		}
	}

	if len(promptParts) > 0 {
		result.PromptExtensions = "\n\n# Skills\n\n" + strings.Join(promptParts, "\n\n---\n\n")
	}

	return result, nil
}

// startCLIAdapter launches the mcp-cli-adapter binary as a subprocess, passing
// the CLI binary name and allowed commands as flags. The adapter speaks MCP
// stdio, so the agent connects to it like any other MCP skill.
func startCLIAdapter(ctx context.Context, s Spec) (ToolCaller, error) {
	cmds := strings.Join(s.CLI.AllowedCommands, ",")
	// The adapter binary must be in PATH inside the agent image.
	return StartMCPProcess(ctx, "mcp-cli-adapter",
		"--binary="+s.CLI.Binary,
		"--commands="+cmds,
	)
}

func connectMCP(ctx context.Context, s Spec) (ToolCaller, error) {
	transport := "stdio"
	if s.Runtime != nil && s.Runtime.Transport != "" {
		transport = s.Runtime.Transport
	}

	switch transport {
	case "stdio":
		// For sidecar MCP servers, the binary path is derived from the skill name.
		// The operator injects the sidecar container; the binary is at a known path.
		// Convention: /usr/local/bin/mcp-{skill-name} or just the image entrypoint.
		// For local testing, the command can be overridden via VOLUND_MCP_{NAME}_CMD.
		cmd := fmt.Sprintf("mcp-%s", s.Name)
		if s.Runtime != nil && s.Runtime.Image != "" {
			// In production the sidecar is already running — we'd connect via
			// a unix socket or localhost port. For now, start as subprocess.
			cmd = s.Runtime.Image
		}
		return StartMCPProcess(ctx, cmd)

	case "http-sse", "http":
		if s.Runtime == nil || s.Runtime.Image == "" {
			return nil, fmt.Errorf("HTTP MCP skill %q requires runtime.image (URL)", s.Name)
		}
		return ConnectMCPHTTP(ctx, s.Runtime.Image)

	default:
		return nil, fmt.Errorf("unsupported MCP transport %q for skill %q", transport, s.Name)
	}
}

func discoverTools(ctx context.Context, skillName string, client ToolCaller) ([]Tool, error) {
	mcpTools, err := client.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	tools := make([]Tool, 0, len(mcpTools))
	for _, t := range mcpTools {
		tools = append(tools, &mcpTool{
			skillName:   skillName,
			name:        t.Name,
			description: t.Description,
			inputSchema: t.InputSchema,
			client:      client,
		})
	}
	return tools, nil
}

// mcpTool wraps a single MCP tool as the agent's Tool interface.
type mcpTool struct {
	skillName   string
	name        string
	description string
	inputSchema map[string]any
	client      ToolCaller
}

func (t *mcpTool) Name() string {
	return t.name
}

func (t *mcpTool) Definition() *volundv1.ToolDefinition {
	schema, _ := json.Marshal(t.inputSchema)
	return &volundv1.ToolDefinition{
		Name:            t.name,
		Description:     t.description,
		InputSchemaJson: string(schema),
	}
}

func (t *mcpTool) Execute(ctx context.Context, inputJSON string) (string, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &args); err != nil {
		return "", fmt.Errorf("parse tool input: %w", err)
	}

	result, isError, err := t.client.CallTool(ctx, t.name, args)
	if err != nil {
		return "", err
	}
	if isError {
		return "", fmt.Errorf("tool %s error: %s", t.name, result)
	}
	return result, nil
}
