// Command mcp-cli-adapter is a generic MCP server that wraps an allowlisted
// CLI binary. Each allowed command becomes an MCP tool. The adapter validates
// that only allowed commands are executed and captures stdout/stderr as the
// tool result.
//
// Usage:
//
//	mcp-cli-adapter --binary=gh --commands="pr view,pr list,pr create,issue list,issue view"
//
// Or via environment:
//
//	MCP_CLI_BINARY=gh MCP_CLI_COMMANDS="pr view,pr list" mcp-cli-adapter
//
// Protocol: JSON-RPC 2.0 over stdio (MCP stdio transport).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// JSON-RPC 2.0 types.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string   `json:"jsonrpc"`
	ID      any      `json:"id,omitempty"`
	Result  any      `json:"result,omitempty"`
	Error   *rpcErr  `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP types.
type serverInfo struct {
	ProtocolVersion string  `json:"protocolVersion"`
	Capabilities    any     `json:"capabilities"`
	ServerInfo      nameVer `json:"serverInfo"`
}

type nameVer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []toolDef `json:"tools"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// cliCommand represents a single allowed CLI command that becomes an MCP tool.
type cliCommand struct {
	// toolName is the MCP tool name, e.g. "gh_pr_view".
	toolName string
	// subcommand is the CLI subcommand, e.g. "pr view".
	subcommand string
	// args are the parsed subcommand tokens, e.g. ["pr", "view"].
	args []string
}

var (
	binary   string
	commands []cliCommand
	timeout  time.Duration
)

func main() {
	binaryFlag := flag.String("binary", "", "CLI binary to wrap")
	commandsFlag := flag.String("commands", "", "Comma-separated allowed subcommands (e.g. 'pr view,pr list')")
	timeoutFlag := flag.Duration("timeout", 30*time.Second, "Command execution timeout")
	flag.Parse()

	binary = envOrFlag(*binaryFlag, "MCP_CLI_BINARY")
	rawCmds := envOrFlag(*commandsFlag, "MCP_CLI_COMMANDS")
	timeout = *timeoutFlag

	if binary == "" || rawCmds == "" {
		fmt.Fprintf(os.Stderr, "mcp-cli-adapter: --binary and --commands are required\n")
		os.Exit(1)
	}

	// Parse allowed commands.
	for _, cmd := range strings.Split(rawCmds, ",") {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		args := strings.Fields(cmd)
		toolName := binary + "_" + strings.Join(args, "_")
		commands = append(commands, cliCommand{
			toolName:   toolName,
			subcommand: cmd,
			args:       args,
		})
	}

	if len(commands) == 0 {
		fmt.Fprintf(os.Stderr, "mcp-cli-adapter: no valid commands provided\n")
		os.Exit(1)
	}

	// MCP stdio loop.
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			writeError(nil, -32700, "parse error: "+err.Error())
			continue
		}

		switch req.Method {
		case "initialize":
			writeResult(req.ID, serverInfo{
				ProtocolVersion: "2024-11-05",
				Capabilities:    map[string]any{"tools": map[string]any{}},
				ServerInfo: nameVer{
					Name:    "mcp-cli-adapter",
					Version: "1.0.0",
				},
			})

		case "notifications/initialized":
			// No response needed.

		case "tools/list":
			writeResult(req.ID, toolsListResult{Tools: buildToolDefs()})

		case "tools/call":
			var params callToolParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				writeError(req.ID, -32602, "invalid params: "+err.Error())
				continue
			}
			handleToolCall(req.ID, params)

		case "ping":
			writeResult(req.ID, map[string]any{})

		default:
			writeError(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

// buildToolDefs creates an MCP tool definition for each allowed command.
func buildToolDefs() []toolDef {
	defs := make([]toolDef, 0, len(commands))
	for _, cmd := range commands {
		defs = append(defs, toolDef{
			Name:        cmd.toolName,
			Description: fmt.Sprintf("Run '%s %s' with additional arguments.", binary, cmd.subcommand),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"args": map[string]any{
						"type":        "string",
						"description": fmt.Sprintf("Additional arguments to pass after '%s %s'. Example: '--repo owner/repo 123'", binary, cmd.subcommand),
					},
				},
			},
		})
	}
	return defs
}

// handleToolCall validates the tool name against the allowlist, then executes.
func handleToolCall(id any, params callToolParams) {
	var matched *cliCommand
	for i := range commands {
		if commands[i].toolName == params.Name {
			matched = &commands[i]
			break
		}
	}

	if matched == nil {
		writeResult(id, callToolResult{
			Content: []contentBlock{{Type: "text", Text: "tool not found: " + params.Name}},
			IsError: true,
		})
		return
	}

	// Build the full command: binary + subcommand args + user args.
	cmdArgs := make([]string, len(matched.args))
	copy(cmdArgs, matched.args)

	if extraArgs, ok := params.Arguments["args"].(string); ok && extraArgs != "" {
		cmdArgs = append(cmdArgs, strings.Fields(extraArgs)...)
	}

	// Execute with timeout.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, cmdArgs...)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		writeResult(id, callToolResult{
			Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("command timed out after %s", timeout)}},
			IsError: true,
		})
		return
	}

	if err != nil {
		// Command failed but still produced output — include both.
		writeResult(id, callToolResult{
			Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("exit error: %s\n\n%s", err, string(output))}},
			IsError: true,
		})
		return
	}

	writeResult(id, callToolResult{
		Content: []contentBlock{{Type: "text", Text: string(output)}},
	})
}

func envOrFlag(flagVal, envKey string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envKey)
}

func writeResult(id any, result any) {
	resp := response{JSONRPC: "2.0", ID: id, Result: result}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

func writeError(id any, code int, message string) {
	resp := response{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: message}}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}
