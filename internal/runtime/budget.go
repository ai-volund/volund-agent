package runtime

import (
	"fmt"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// minPreservedTail is the minimum number of recent messages that trimMessages
// will never remove, regardless of budget pressure. This ensures the most
// recent user+assistant exchange (plus tool results) always survives.
const minPreservedTail = 4

// estimateTokens returns a rough token count for a single LLM message.
// Heuristic: 4 characters ~ 1 token. Good enough for budget decisions.
func estimateTokens(msg *volundv1.LLMMessage) int {
	if msg == nil {
		return 0
	}
	chars := 0
	// Count role string (small but consistent).
	chars += len(msg.Role)
	for _, block := range msg.Content {
		switch b := block.Block.(type) {
		case *volundv1.ContentBlock_Text:
			chars += len(b.Text)
		case *volundv1.ContentBlock_ToolUse:
			chars += len(b.ToolUse.GetName())
			chars += len(b.ToolUse.GetInputJson())
		case *volundv1.ContentBlock_ToolResult:
			chars += len(b.ToolResult.GetContent())
		}
	}
	tokens := chars / 4
	if tokens == 0 && chars > 0 {
		tokens = 1
	}
	return tokens
}

// trimMessages trims the conversation history so total estimated tokens stay
// within maxTokens. Rules:
//
//  1. The system prompt (first message if role=="system") is ALWAYS kept.
//  2. The last minPreservedTail messages are ALWAYS kept.
//  3. Oldest non-system messages are removed first.
//  4. Removed messages are replaced with a single truncation notice.
func trimMessages(messages []*volundv1.LLMMessage, maxTokens int) []*volundv1.LLMMessage {
	if len(messages) == 0 || maxTokens <= 0 {
		return messages
	}

	// Calculate total tokens.
	total := 0
	for _, m := range messages {
		total += estimateTokens(m)
	}

	// Under budget — nothing to do.
	if total <= maxTokens {
		return messages
	}

	// Identify protected regions.
	hasSystem := len(messages) > 0 && messages[0].Role == "system"

	// The trimmable window starts after the system prompt and ends before
	// the preserved tail.
	trimStart := 0
	if hasSystem {
		trimStart = 1
	}

	// Ensure we never trim into the tail.
	tailStart := len(messages) - minPreservedTail
	if tailStart < trimStart {
		// Not enough messages to trim — everything is protected.
		return messages
	}

	// Remove messages from trimStart..tailStart-1 (oldest first) until under budget.
	removed := 0
	for i := trimStart; i < tailStart && total > maxTokens; i++ {
		total -= estimateTokens(messages[i])
		removed++
	}

	if removed == 0 {
		return messages
	}

	// Build the truncation notice.
	notice := &volundv1.LLMMessage{
		Role: "system",
		Content: []*volundv1.ContentBlock{
			{Block: &volundv1.ContentBlock_Text{
				Text: fmt.Sprintf("[Earlier conversation history truncated \u2014 %d messages removed to fit context window]", removed),
			}},
		},
	}

	// Assemble: system (if any) + notice + surviving middle + tail.
	var result []*volundv1.LLMMessage
	if hasSystem {
		result = append(result, messages[0])
	}
	result = append(result, notice)
	// Surviving middle messages (trimStart+removed .. tailStart-1).
	result = append(result, messages[trimStart+removed:tailStart]...)
	// Preserved tail.
	result = append(result, messages[tailStart:]...)

	return result
}
