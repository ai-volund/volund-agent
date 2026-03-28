package runtime

import (
	"testing"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"

	"github.com/ai-volund/volund-agent/internal/stream"
	"github.com/ai-volund/volund-agent/internal/tools"
)

func TestBuildMessages(t *testing.T) {
	task := &Task{
		SystemPrompt: "You are a helpful agent.",
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "hello"}}},
		},
	}
	msgs := buildMessages(task)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	sysText := getTextContent(msgs[0])
	if sysText != "You are a helpful agent." {
		t.Errorf("system prompt = %q, want %q", sysText, "You are a helpful agent.")
	}
	if msgs[1].Role != "user" {
		t.Errorf("second message role = %q, want user", msgs[1].Role)
	}
}

func TestBuildMessages_NoSystemPrompt(t *testing.T) {
	task := &Task{
		SystemPrompt: "",
		Messages: []Message{
			{Role: "user", Content: []Block{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []Block{{Type: "text", Text: "hello"}}},
		},
	}
	msgs := buildMessages(task)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (no system), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("first message role = %q, want user", msgs[0].Role)
	}
}

func TestBuildAssistantMessage_TextOnly(t *testing.T) {
	msg := buildAssistantMessage("Hello, world!", nil)
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(msg.Content))
	}
	tb, ok := msg.Content[0].Block.(*volundv1.ContentBlock_Text)
	if !ok {
		t.Fatal("expected text block")
	}
	if tb.Text != "Hello, world!" {
		t.Errorf("text = %q, want %q", tb.Text, "Hello, world!")
	}
}

func TestBuildAssistantMessage_WithToolCalls(t *testing.T) {
	calls := []tools.Call{
		{ID: "tc_1", Name: "run_code", InputJSON: `{"code":"1+1"}`},
		{ID: "tc_2", Name: "web_search", InputJSON: `{"query":"go"}`},
	}
	msg := buildAssistantMessage("thinking...", calls)
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	// 1 text block + 2 tool_use blocks = 3
	if len(msg.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(msg.Content))
	}
	// First should be text.
	if _, ok := msg.Content[0].Block.(*volundv1.ContentBlock_Text); !ok {
		t.Error("first block should be text")
	}
	// Second and third should be tool_use.
	for i := 1; i <= 2; i++ {
		tu, ok := msg.Content[i].Block.(*volundv1.ContentBlock_ToolUse)
		if !ok {
			t.Errorf("block %d should be tool_use", i)
			continue
		}
		if tu.ToolUse.Id != calls[i-1].ID {
			t.Errorf("tool call %d ID = %q, want %q", i, tu.ToolUse.Id, calls[i-1].ID)
		}
	}
}

func TestBuildAssistantMessage_EmptyTextWithTools(t *testing.T) {
	calls := []tools.Call{
		{ID: "tc_1", Name: "run_code", InputJSON: `{}`},
	}
	msg := buildAssistantMessage("", calls)
	// Empty text should not produce a text block.
	if len(msg.Content) != 1 {
		t.Fatalf("expected 1 content block (tool_use only), got %d", len(msg.Content))
	}
	if _, ok := msg.Content[0].Block.(*volundv1.ContentBlock_ToolUse); !ok {
		t.Error("expected tool_use block")
	}
}

func TestDrainSteering_Empty(t *testing.T) {
	ch := make(chan stream.SteerMessage, 8)
	msgs := []*volundv1.LLMMessage{
		textMsg("user", "hello"),
	}
	result := drainSteering(msgs, ch)
	if len(result) != 1 {
		t.Fatalf("expected 1 message (no steering), got %d", len(result))
	}
}

func TestDrainSteering_WithMessages(t *testing.T) {
	ch := make(chan stream.SteerMessage, 8)
	ch <- stream.SteerMessage{Content: "actually, do X instead"}
	ch <- stream.SteerMessage{Content: "and also Y"}

	msgs := []*volundv1.LLMMessage{
		textMsg("system", "sys"),
		textMsg("user", "original request"),
	}
	result := drainSteering(msgs, ch)
	if len(result) != 4 {
		t.Fatalf("expected 4 messages (2 original + 2 steering), got %d", len(result))
	}
	// The injected steering messages should be user role.
	if result[2].Role != "user" {
		t.Errorf("steering msg 1 role = %q, want user", result[2].Role)
	}
	steerText := getTextContent(result[2])
	if steerText != "actually, do X instead" {
		t.Errorf("steering msg 1 text = %q", steerText)
	}
	steerText2 := getTextContent(result[3])
	if steerText2 != "and also Y" {
		t.Errorf("steering msg 2 text = %q", steerText2)
	}
}

func TestDrainSteering_ClosedChannel(t *testing.T) {
	ch := make(chan stream.SteerMessage, 8)
	ch <- stream.SteerMessage{Content: "msg before close"}
	close(ch)

	msgs := []*volundv1.LLMMessage{
		textMsg("user", "hello"),
	}
	result := drainSteering(msgs, ch)
	// Should drain the one message and then detect the closed channel.
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestExtractText_TextOnly(t *testing.T) {
	msg := buildAssistantMessage("Hello, world!", nil)
	text := extractText(msg)
	if text != "Hello, world!" {
		t.Fatalf("expected 'Hello, world!', got %q", text)
	}
}

func TestExtractText_WithToolCalls(t *testing.T) {
	calls := []tools.Call{
		{ID: "tc_1", Name: "run_code", InputJSON: `{"code":"1+1"}`},
	}
	msg := buildAssistantMessage("thinking...", calls)
	text := extractText(msg)
	if text != "thinking..." {
		t.Fatalf("expected 'thinking...', got %q", text)
	}
}

func TestExtractText_EmptyText(t *testing.T) {
	calls := []tools.Call{
		{ID: "tc_1", Name: "run_code", InputJSON: `{}`},
	}
	msg := buildAssistantMessage("", calls)
	text := extractText(msg)
	if text != "" {
		t.Fatalf("expected empty string, got %q", text)
	}
}

func TestExtractText_Nil(t *testing.T) {
	text := extractText(nil)
	if text != "" {
		t.Fatalf("expected empty string for nil message, got %q", text)
	}
}
