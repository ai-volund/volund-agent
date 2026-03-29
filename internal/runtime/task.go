package runtime

import (
	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"

	"github.com/ai-volund/volund-agent/internal/skill"
)

// Task is the JSON payload published to volund.pool.{profileName} by the
// control plane when dispatching work to an agent pod.
type Task struct {
	TaskID         string `json:"task_id"`
	ConversationID string `json:"conversation_id"`
	InstanceID     string `json:"instance_id,omitempty"`
	TenantID       string `json:"tenant_id"`
	AgentID        string `json:"agent_id,omitempty"`
	ProfileName    string `json:"profile_name"`
	ProfileType    string `json:"profile_type"` // "orchestrator" | "specialist"

	// LLM config — zero values fall back to agent defaults from config.
	SystemPrompt  string  `json:"system_prompt,omitempty"`
	Provider      string  `json:"provider,omitempty"`
	Model         string  `json:"model,omitempty"`
	MaxTokens     int32   `json:"max_tokens,omitempty"`
	Temperature   float64 `json:"temperature,omitempty"`
	MaxToolRounds int     `json:"max_tool_rounds,omitempty"`

	// TraceContext carries W3C trace context headers for distributed tracing.
	TraceContext map[string]string `json:"trace_context,omitempty"`

	// Messages is the full conversation history in transport format.
	// Plain JSON structs (not proto) to avoid oneof serialisation issues.
	Messages []Message `json:"messages"`

	// EnabledTools restricts built-ins. Empty = all registered tools.
	EnabledTools []string `json:"enabled_tools,omitempty"`

	// Skills are resolved skill specs from the control plane.
	// Prompt skills are appended to the system prompt; MCP/CLI skills
	// are connected and their tools registered dynamically.
	Skills []skill.Spec `json:"skills,omitempty"`

	// Credentials are scoped, short-lived tokens issued by the credential broker.
	Credentials []TaskCredential `json:"credentials,omitempty"`

	// Delegation tracking — set by the orchestrator when dispatching sub-tasks.
	ParentTaskID    string `json:"parent_task_id,omitempty"`
	DelegationDepth int    `json:"delegation_depth,omitempty"` // 0 = top-level
}

// TaskCredential is a short-lived credential token attached to a task.
type TaskCredential struct {
	Provider  string   `json:"provider"`
	Token     string   `json:"token"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in"`
}

// Message is the transport-friendly LLM message format.
// Mirrors dispatch.Message in volund — both sides must use matching JSON tags.
type Message struct {
	Role    string  `json:"role"` // "system" | "user" | "assistant" | "tool"
	Content []Block `json:"content"`
}

// Block is a single content block within a Message.
type Block struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result" | "attachment"

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ToolUseID string `json:"tool_use_id,omitempty"`
	Name      string `json:"name,omitempty"`
	InputJSON string `json:"input_json,omitempty"`

	// tool_result
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`

	// attachment (file upload)
	AttachmentID string `json:"attachment_id,omitempty"`
	URL          string `json:"url,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

// toProtoMessages converts transport messages to proto LLMMessages for the LLM router.
func toProtoMessages(msgs []Message) []*volundv1.LLMMessage {
	out := make([]*volundv1.LLMMessage, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]*volundv1.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				blocks = append(blocks, &volundv1.ContentBlock{
					Block: &volundv1.ContentBlock_Text{Text: b.Text},
				})
			case "tool_use":
				blocks = append(blocks, &volundv1.ContentBlock{
					Block: &volundv1.ContentBlock_ToolUse{
						ToolUse: &volundv1.ToolUseContent{
							Id:        b.ToolUseID,
							Name:      b.Name,
							InputJson: b.InputJSON,
						},
					},
				})
			case "tool_result":
				blocks = append(blocks, &volundv1.ContentBlock{
					Block: &volundv1.ContentBlock_ToolResult{
						ToolResult: &volundv1.ToolResultContent{
							ToolUseId: b.ToolUseID,
							Content:   b.Content,
							IsError:   b.IsError,
						},
					},
				})
			case "attachment":
				// Attachments are rendered as text for the LLM with metadata.
				desc := "[Attached file: " + b.FileName + " (" + b.MimeType + ")]"
				if b.URL != "" {
					desc += " URL: " + b.URL
				}
				blocks = append(blocks, &volundv1.ContentBlock{
					Block: &volundv1.ContentBlock_Text{Text: desc},
				})
			}
		}
		out = append(out, &volundv1.LLMMessage{
			Role:    m.Role,
			Content: blocks,
		})
	}
	return out
}
