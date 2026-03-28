// Package skill implements skill loading for the agent runtime.
// It resolves skill definitions (prompt, MCP, CLI) and integrates them
// into the agent's tool registry and system prompt.
package skill

// Spec mirrors the Skill CRD spec — passed to the agent at claim time
// via the task payload or environment. The agent runtime doesn't talk to
// the K8s API directly; the control plane resolves skills and includes
// them in the task dispatch.
type Spec struct {
	Name        string       `json:"name"`
	Type        string       `json:"type"` // prompt, mcp, cli
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Prompt      string       `json:"prompt,omitempty"`
	Runtime     *RuntimeSpec `json:"runtime,omitempty"`
	CLI         *CLISpec     `json:"cli,omitempty"`
	Parameters  []Parameter  `json:"parameters,omitempty"`
}

type RuntimeSpec struct {
	Image     string `json:"image,omitempty"`
	Mode      string `json:"mode,omitempty"`      // sidecar, shared
	Transport string `json:"transport,omitempty"` // stdio, http-sse
}

type CLISpec struct {
	Binary          string   `json:"binary"`
	AllowedCommands []string `json:"allowedCommands"`
}

type Parameter struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required,omitempty"`
}
