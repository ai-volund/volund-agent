package skill

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

// MCPClient communicates with a single MCP server over stdio (JSON-RPC 2.0).
type MCPClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Scanner

	mu       sync.Mutex
	nextID   atomic.Int64
	pending  map[int64]chan json.RawMessage
	stopOnce sync.Once
	done     chan struct{}
}

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []mcpToolDef `json:"tools"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// StartMCPProcess starts an MCP server as a subprocess and performs the
// initialize handshake. Returns the connected client.
// Optional env vars can be passed to the subprocess (e.g. CREDENTIAL_TOKEN).
func StartMCPProcess(ctx context.Context, command string, args ...string) (*MCPClient, error) {
	return StartMCPProcessWithEnv(ctx, nil, command, args...)
}

// StartMCPProcessWithEnv starts an MCP server subprocess with additional env vars.
func StartMCPProcessWithEnv(ctx context.Context, env []string, command string, args ...string) (*MCPClient, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Discard stderr — MCP servers may log there.
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start MCP process %q: %w", command, err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	c := &MCPClient{
		cmd:     cmd,
		stdin:   stdin,
		reader:  scanner,
		pending: make(map[int64]chan json.RawMessage),
		done:    make(chan struct{}),
	}

	go c.readLoop()

	// Initialize handshake.
	initResult, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "volund-agent",
			"version": "1.0.0",
		},
	})
	if err != nil {
		c.Stop()
		return nil, fmt.Errorf("MCP initialize: %w", err)
	}

	slog.Info("MCP server initialized", "command", command, "result", string(initResult))

	// Send initialized notification (no response expected).
	c.notify("notifications/initialized", nil)

	return c, nil
}

// ListTools calls tools/list on the MCP server.
func (c *MCPClient) ListTools(ctx context.Context) ([]mcpToolDef, error) {
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result toolsListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list: %w", err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server.
func (c *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, bool, error) {
	raw, err := c.call(ctx, "tools/call", callToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return "", true, err
	}
	var result callToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", true, fmt.Errorf("parse tools/call: %w", err)
	}
	// Concatenate text blocks.
	var text string
	for _, b := range result.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return text, result.IsError, nil
}

// Stop terminates the MCP server process.
func (c *MCPClient) Stop() {
	c.stopOnce.Do(func() {
		c.stdin.Close()
		c.cmd.Process.Kill()
		c.cmd.Wait()
		close(c.done)
	})
}

func (c *MCPClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := fmt.Fprintf(c.stdin, "%s\n", data); err != nil {
		return nil, fmt.Errorf("write to MCP: %w", err)
	}

	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("MCP process exited")
	}
}

func (c *MCPClient) notify(method string, params any) {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, _ := json.Marshal(req)
	fmt.Fprintf(c.stdin, "%s\n", data)
}

func (c *MCPClient) readLoop() {
	for c.reader.Scan() {
		line := c.reader.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp jsonrpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			slog.Warn("MCP: failed to parse response", "error", err, "line", string(line))
			continue
		}

		if resp.Error != nil {
			slog.Warn("MCP RPC error", "id", resp.ID, "code", resp.Error.Code, "message", resp.Error.Message)
			// Deliver the error as an empty result so the caller unblocks.
			c.mu.Lock()
			if ch, ok := c.pending[resp.ID]; ok {
				errJSON, _ := json.Marshal(map[string]string{"error": resp.Error.Message})
				ch <- errJSON
			}
			c.mu.Unlock()
			continue
		}

		c.mu.Lock()
		if ch, ok := c.pending[resp.ID]; ok {
			ch <- resp.Result
		}
		c.mu.Unlock()
	}
}
