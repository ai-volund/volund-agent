// Command mcp-echo is a minimal MCP server for testing the skill sidecar
// pipeline. It exposes a single "echo" tool that returns its input.
//
// Protocol: JSON-RPC 2.0 over stdio (MCP stdio transport).
// Each message is a single JSON line terminated by \n.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// JSON-RPC 2.0 types.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP types.
type serverInfo struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ServerInfo      nameVer    `json:"serverInfo"`
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

var echoTool = toolDef{
	Name:        "echo",
	Description: "Returns the input message back unchanged. Useful for testing MCP connectivity.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "The message to echo back",
			},
		},
		"required": []string{"message"},
	},
}

var uppercaseTool = toolDef{
	Name:        "uppercase",
	Description: "Converts the input message to uppercase.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "The text to convert to uppercase",
			},
		},
		"required": []string{"text"},
	},
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	// Increase buffer for large messages.
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
				Capabilities: map[string]any{
					"tools": map[string]any{},
				},
				ServerInfo: nameVer{
					Name:    "mcp-echo",
					Version: "1.0.0",
				},
			})

		case "notifications/initialized":
			// Client acknowledged — no response needed for notifications.

		case "tools/list":
			writeResult(req.ID, toolsListResult{
				Tools: []toolDef{echoTool, uppercaseTool},
			})

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

func handleToolCall(id any, params callToolParams) {
	switch params.Name {
	case "echo":
		msg, _ := params.Arguments["message"].(string)
		writeResult(id, callToolResult{
			Content: []contentBlock{{Type: "text", Text: msg}},
		})

	case "uppercase":
		text, _ := params.Arguments["text"].(string)
		writeResult(id, callToolResult{
			Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("%s", upperASCII(text))}},
		})

	default:
		writeResult(id, callToolResult{
			Content: []contentBlock{{Type: "text", Text: "unknown tool: " + params.Name}},
			IsError: true,
		})
	}
}

func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func writeResult(id any, result any) {
	resp := response{JSONRPC: "2.0", ID: id, Result: result}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}

func writeError(id any, code int, message string) {
	resp := response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
	data, _ := json.Marshal(resp)
	fmt.Fprintf(os.Stdout, "%s\n", data)
}
