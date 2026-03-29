package safety

import (
	"context"
	"strings"

	"github.com/ai-volund/volund-agent/internal/tools"
)

// SecretRedactionHook is an AfterHook that redacts secrets from tool output.
// This prevents credential leakage when tool results are sent back to the LLM.
func SecretRedactionHook(_ context.Context, _ tools.Call, result tools.Result) (tools.Result, error) {
	result.Content = RedactSecrets(result.Content)
	return result, nil
}

// OutputSizeLimitHook returns an AfterHook that truncates oversized tool output
// to prevent context window exhaustion attacks where a malicious tool returns
// extremely large payloads to crowd out system instructions.
func OutputSizeLimitHook(maxBytes int) tools.AfterHook {
	return func(_ context.Context, _ tools.Call, result tools.Result) (tools.Result, error) {
		if len(result.Content) > maxBytes {
			result.Content = result.Content[:maxBytes] + "\n...[truncated]"
		}
		return result, nil
	}
}

// ToolArgumentValidationHook is a BeforeHook that validates tool arguments
// don't contain injection attempts. It normalizes unicode in the arguments
// and checks for known injection patterns.
func ToolArgumentValidationHook(_ context.Context, call tools.Call) (bool, string, error) {
	normalized := normalizeUnicode(call.InputJSON)
	lower := strings.ToLower(normalized)
	if containsInjectionPattern(lower) {
		return true, "tool arguments contain suspicious injection patterns", nil
	}
	return false, "", nil
}
