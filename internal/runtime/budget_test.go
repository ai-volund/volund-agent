package runtime

import (
	"strings"
	"testing"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// helper to build a simple text message.
func textMsg(role, text string) *volundv1.LLMMessage {
	return &volundv1.LLMMessage{
		Role: role,
		Content: []*volundv1.ContentBlock{
			{Block: &volundv1.ContentBlock_Text{Text: text}},
		},
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		msg  *volundv1.LLMMessage
		want int
	}{
		{
			name: "nil message",
			msg:  nil,
			want: 0,
		},
		{
			name: "empty message",
			msg:  &volundv1.LLMMessage{Role: "user"},
			want: 1, // "user" = 4 chars = 1 token
		},
		{
			name: "short text",
			msg:  textMsg("user", "hello world"),
			// "user" (4) + "hello world" (11) = 15 chars => 3 tokens
			want: 3,
		},
		{
			name: "tool use block",
			msg: &volundv1.LLMMessage{
				Role: "assistant",
				Content: []*volundv1.ContentBlock{
					{Block: &volundv1.ContentBlock_ToolUse{
						ToolUse: &volundv1.ToolUseContent{
							Name:      "run_code",
							InputJson: `{"code":"print('hi')"}`,
						},
					}},
				},
			},
			// "assistant" (9) + "run_code" (8) + input (22) = 39 => 9 tokens
			want: 9,
		},
		{
			name: "tool result block",
			msg: &volundv1.LLMMessage{
				Role: "tool",
				Content: []*volundv1.ContentBlock{
					{Block: &volundv1.ContentBlock_ToolResult{
						ToolResult: &volundv1.ToolResultContent{
							Content: "success: output was hello",
						},
					}},
				},
			},
			// "tool" (4) + content (25) = 29 => 7 tokens
			want: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateTokens(tt.msg)
			if got != tt.want {
				t.Errorf("estimateTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTrimMessages_UnderBudget(t *testing.T) {
	msgs := []*volundv1.LLMMessage{
		textMsg("system", "you are helpful"),
		textMsg("user", "hello"),
		textMsg("assistant", "hi there"),
	}
	result := trimMessages(msgs, 100000)
	if len(result) != len(msgs) {
		t.Fatalf("expected %d messages unchanged, got %d", len(msgs), len(result))
	}
}

func TestTrimMessages_OverBudget(t *testing.T) {
	// Build a conversation that exceeds a small budget.
	msgs := []*volundv1.LLMMessage{
		textMsg("system", "system prompt"),
		textMsg("user", strings.Repeat("a", 400)),   // ~100 tokens
		textMsg("assistant", strings.Repeat("b", 400)), // ~100 tokens
		textMsg("user", strings.Repeat("c", 400)),      // ~100 tokens
		textMsg("assistant", strings.Repeat("d", 400)), // ~100 tokens
		textMsg("user", "recent question"),
		textMsg("assistant", "recent answer"),
		textMsg("user", "follow up"),
		textMsg("assistant", "follow up answer"),
	}

	// Budget that forces trimming of the middle bulk messages.
	// System + tail(4) ~ small, but the 4 bulk messages are ~400 tokens.
	result := trimMessages(msgs, 50)

	// System prompt must be first.
	if result[0].Role != "system" || getTextContent(result[0]) != "system prompt" {
		t.Error("system prompt was not preserved at index 0")
	}

	// Last 4 messages must be preserved.
	tail := result[len(result)-4:]
	expectedTail := []string{"recent question", "recent answer", "follow up", "follow up answer"}
	for i, msg := range tail {
		got := getTextContent(msg)
		if got != expectedTail[i] {
			t.Errorf("tail[%d] = %q, want %q", i, got, expectedTail[i])
		}
	}

	// Should be fewer messages than original (some were trimmed).
	if len(result) >= len(msgs) {
		t.Errorf("expected trimming, got %d messages (original %d)", len(result), len(msgs))
	}
}

func TestTrimMessages_KeepsRecentMessages(t *testing.T) {
	// Only 5 messages total: system + 4 messages. All should be kept even if over budget.
	msgs := []*volundv1.LLMMessage{
		textMsg("system", "sys"),
		textMsg("user", strings.Repeat("x", 4000)),
		textMsg("assistant", strings.Repeat("y", 4000)),
		textMsg("user", strings.Repeat("z", 4000)),
		textMsg("assistant", strings.Repeat("w", 4000)),
	}
	// Tiny budget, but nothing trimmable (tail of 4 == all non-system messages).
	result := trimMessages(msgs, 10)
	if len(result) != len(msgs) {
		t.Fatalf("expected all %d messages preserved (nothing trimmable), got %d", len(msgs), len(result))
	}
}

func TestTrimMessages_PreservesSystemPrompt(t *testing.T) {
	msgs := []*volundv1.LLMMessage{
		textMsg("system", "important system instructions"),
		textMsg("user", strings.Repeat("a", 2000)),
		textMsg("assistant", strings.Repeat("b", 2000)),
		textMsg("user", strings.Repeat("c", 2000)),
		textMsg("assistant", strings.Repeat("d", 2000)),
		textMsg("user", "q1"),
		textMsg("assistant", "a1"),
		textMsg("user", "q2"),
		textMsg("assistant", "a2"),
	}
	result := trimMessages(msgs, 50)
	if result[0].Role != "system" {
		t.Fatal("first message should be system")
	}
	if getTextContent(result[0]) != "important system instructions" {
		t.Error("system prompt content was modified")
	}
}

func TestTrimMessages_InsertsTruncationNotice(t *testing.T) {
	msgs := []*volundv1.LLMMessage{
		textMsg("system", "sys"),
		textMsg("user", strings.Repeat("a", 2000)),
		textMsg("assistant", strings.Repeat("b", 2000)),
		textMsg("user", strings.Repeat("c", 2000)),
		textMsg("assistant", strings.Repeat("d", 2000)),
		textMsg("user", "q"),
		textMsg("assistant", "a"),
		textMsg("user", "q2"),
		textMsg("assistant", "a2"),
	}
	result := trimMessages(msgs, 50)

	// The truncation notice should be after the system prompt.
	if len(result) < 2 {
		t.Fatal("expected at least system + notice + tail")
	}
	notice := getTextContent(result[1])
	if !strings.Contains(notice, "Earlier conversation history truncated") {
		t.Errorf("expected truncation notice, got: %q", notice)
	}
	if !strings.Contains(notice, "messages removed") {
		t.Errorf("notice should mention messages removed, got: %q", notice)
	}
}

func TestTrimMessages_EmptyMessages(t *testing.T) {
	result := trimMessages(nil, 100)
	if result != nil {
		t.Errorf("expected nil for nil input, got %v", result)
	}

	result = trimMessages([]*volundv1.LLMMessage{}, 100)
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %d messages", len(result))
	}
}

// getTextContent extracts the first text block from a message.
func getTextContent(msg *volundv1.LLMMessage) string {
	for _, block := range msg.Content {
		if tb, ok := block.Block.(*volundv1.ContentBlock_Text); ok {
			return tb.Text
		}
	}
	return ""
}
