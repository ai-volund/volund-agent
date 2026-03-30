// Package tools provides the tool registry, execution hooks, and Tool interface
// for the agent runtime. Built-in tools live in the builtin sub-package.
package tools

import (
	"context"
	"fmt"
	"log/slog"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// Tool is the interface all built-in and registered tools must implement.
type Tool interface {
	// Name returns the unique identifier used in LLM tool calls.
	Name() string
	// Definition returns the proto ToolDefinition for sending to the LLM.
	Definition() *volundv1.ToolDefinition
	// Execute runs the tool with the given JSON-encoded input and returns the result.
	Execute(ctx context.Context, inputJSON string) (string, error)
}

// Call represents a tool invocation from the LLM.
type Call struct {
	ID        string // tool_use_id from the LLM
	Name      string
	InputJSON string
}

// Result is the outcome of a tool execution.
type Result struct {
	CallID  string
	Content string
	IsError bool
}

// BeforeHook is called before every tool execution.
// Return block=true with a reason to prevent execution (e.g. policy enforcement).
type BeforeHook func(ctx context.Context, call Call) (block bool, reason string, err error)

// AfterHook is called after every tool execution.
// Can transform or redact the result (e.g. strip secrets from output).
type AfterHook func(ctx context.Context, call Call, result Result) (Result, error)

// Registry holds registered tools and execution hooks.
type Registry struct {
	tools  map[string]Tool
	before []BeforeHook
	after  []AfterHook
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a Tool to the registry. Panics on duplicate name.
func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; exists {
		panic(fmt.Sprintf("tools: duplicate tool name %q", t.Name()))
	}
	r.tools[t.Name()] = t
	slog.Debug("tool registered", "name", t.Name())
}

// AddBeforeHook appends a hook that runs before every tool execution.
func (r *Registry) AddBeforeHook(h BeforeHook) {
	r.before = append(r.before, h)
}

// AddAfterHook appends a hook that runs after every tool execution.
func (r *Registry) AddAfterHook(h AfterHook) {
	r.after = append(r.after, h)
}

// Definitions returns proto ToolDefinitions for all registered tools,
// suitable for passing to a StreamChatRequest.
func (r *Registry) Definitions() []*volundv1.ToolDefinition {
	defs := make([]*volundv1.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Definition())
	}
	return defs
}

// Has reports whether a tool with the given name is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Replace registers a tool, overwriting any existing tool with the same name.
// Used by skill loading to let MCP/CLI tools override builtins of the same name.
func (r *Registry) Replace(t Tool) {
	if _, exists := r.tools[t.Name()]; exists {
		slog.Debug("tool replaced", "name", t.Name())
	}
	r.tools[t.Name()] = t
}

// Execute runs a tool call through the before hooks, tool implementation,
// and after hooks. Never returns an error — failures are encoded in Result.IsError.
func (r *Registry) Execute(ctx context.Context, call Call) Result {
	// Run before hooks.
	for _, h := range r.before {
		block, reason, err := h(ctx, call)
		if err != nil {
			slog.Warn("tool before-hook error", "tool", call.Name, "error", err)
			return Result{CallID: call.ID, Content: "hook error: " + err.Error(), IsError: true}
		}
		if block {
			slog.Info("tool call blocked by hook", "tool", call.Name, "reason", reason)
			return Result{CallID: call.ID, Content: "blocked: " + reason, IsError: true}
		}
	}

	// Look up and execute the tool.
	t, ok := r.tools[call.Name]
	if !ok {
		return Result{
			CallID:  call.ID,
			Content: fmt.Sprintf("unknown tool: %q", call.Name),
			IsError: true,
		}
	}

	content, execErr := t.Execute(ctx, call.InputJSON)
	result := Result{CallID: call.ID, Content: content}
	if execErr != nil {
		result.Content = execErr.Error()
		result.IsError = true
	}

	// Run after hooks.
	for _, h := range r.after {
		transformed, err := h(ctx, call, result)
		if err != nil {
			slog.Warn("tool after-hook error", "tool", call.Name, "error", err)
			// Log but continue — after hook errors don't clobber the actual result.
			continue
		}
		result = transformed
	}

	return result
}
