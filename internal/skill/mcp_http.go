package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// MCPHTTPClient communicates with an MCP server over HTTP (JSON-RPC 2.0).
// Each request is a POST to the server's endpoint. This is the transport
// used for external/shared MCP servers.
type MCPHTTPClient struct {
	baseURL string
	client  *http.Client
	nextID  atomic.Int64
}

// ConnectMCPHTTP creates a client for an HTTP-based MCP server and performs
// the initialize handshake.
func ConnectMCPHTTP(ctx context.Context, baseURL string) (*MCPHTTPClient, error) {
	c := &MCPHTTPClient{
		baseURL: baseURL,
		client:  &http.Client{},
	}

	initResult, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "volund-agent",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return nil, fmt.Errorf("MCP HTTP initialize: %w", err)
	}

	slog.Info("MCP HTTP server initialized", "url", baseURL, "result", string(initResult))

	// Send initialized notification.
	c.notify(ctx, "notifications/initialized", nil)

	return c, nil
}

// ListTools calls tools/list on the MCP server.
func (c *MCPHTTPClient) ListTools(ctx context.Context) ([]mcpToolDef, error) {
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
func (c *MCPHTTPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, bool, error) {
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
	var text string
	for _, b := range result.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return text, result.IsError, nil
}

// Stop is a no-op for HTTP clients (no process to kill).
func (c *MCPHTTPClient) Stop() {}

func (c *MCPHTTPClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MCP HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var rpcResp jsonrpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("MCP RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

func (c *MCPHTTPClient) notify(ctx context.Context, method string, params any) {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	body, _ := json.Marshal(req)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return
	}
	resp.Body.Close()
}
