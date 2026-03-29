package runtime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"

	"github.com/ai-volund/volund-agent/internal/safety"
	"github.com/ai-volund/volund-agent/internal/stream"
	"github.com/ai-volund/volund-agent/internal/tools"
)

// ErrMaxRoundsExceeded is returned when the inner loop hits its tool round cap.
var ErrMaxRoundsExceeded = errors.New("max tool rounds exceeded")

// processTask runs the full agent turn for a single task.
//
// Outer loop: after the inner loop completes, check the task channel for a
// follow-up message (user replied before the pod went idle). If one is waiting,
// re-enter the inner loop without releasing the pod. In v1 the control plane
// sends a fresh full-context task for follow-ups, so we accept the same Task
// payload on the same channel.
func (r *Runtime) processTask(ctx context.Context, task *Task, taskCh <-chan []byte, steerCh <-chan stream.SteerMessage) error {
	log := slog.With("task_id", task.TaskID, "conv_id", task.ConversationID, "profile_type", task.ProfileType)
	log.Info("task started")

	// Inject task credentials into context for tool access.
	if len(task.Credentials) > 0 {
		creds := make([]tools.Credential, len(task.Credentials))
		for i, c := range task.Credentials {
			creds[i] = tools.Credential{
				Provider:  c.Provider,
				Token:     c.Token,
				Scopes:    c.Scopes,
				ExpiresIn: c.ExpiresIn,
			}
		}
		ctx = tools.WithCredentials(ctx, creds)
		log.Info("task credentials injected", "count", len(creds))
	}

	// accumulatedContent collects the final assistant text across inner-loop turns.
	// Used by specialist agents to publish the result back to the orchestrator.
	var accumulatedContent string

	r.stream.Publish(task.ConversationID, stream.Event{
		Type:        stream.EventAgentStart,
		AgentID:     task.AgentID,
		InstanceID:  r.cfg.InstanceID,
		ConvID:      task.ConversationID,
		ProfileType: task.ProfileType,
	})
	defer func() {
		r.stream.Publish(task.ConversationID, stream.Event{
			Type:    stream.EventAgentEnd,
			AgentID: task.AgentID,
			ConvID:  task.ConversationID,
		})

		// If this is a specialist task with a task ID, publish the accumulated
		// response as a task result so the orchestrator's inbox gets populated.
		if task.TaskID != "" && r.stream != nil {
			result := TaskResult{
				TaskID:  task.TaskID,
				Status:  "complete",
				Content: accumulatedContent,
			}
			data, err := json.Marshal(result)
			if err != nil {
				log.Warn("failed to marshal task result", "error", err)
			} else if err := r.stream.PublishTaskResult(task.TaskID, data); err != nil {
				log.Warn("failed to publish task result", "error", err)
			} else {
				log.Info("task result published", "task_id", task.TaskID)
			}
		}

		log.Info("task finished")
	}()

	// Scope session memory to this conversation.
	if r.memory != nil {
		r.memory.SetConversation(task.ConversationID)
	}

	// Apply config defaults for any zero-value fields in the task.
	r.applyDefaults(task)

	// Load skills: append prompt skills to system prompt, connect MCP sidecars.
	task.SystemPrompt = r.loadSkills(ctx, task)

	// Append content safety policy to instruct the LLM to treat external
	// data markers as untrusted. This is a defense-in-depth measure.
	task.SystemPrompt += safety.SystemPromptSuffix()

	messages := buildMessages(task)

	// Auto-inject session history and long-term memories into the system prompt.
	if r.memory != nil {
		needsRebuild := false

		// Inject recent session history for continuity.
		if hist, err := r.memory.GetHistory(ctx, 20); err == nil && hist != "" {
			task.SystemPrompt += hist
			needsRebuild = true
			log.Info("session history injected into context")
		}

		// Inject relevant long-term memories.
		if len(task.Messages) > 0 {
			lastMsg := task.Messages[len(task.Messages)-1]
			if lastMsg.Role == "user" && len(lastMsg.Content) > 0 {
				query := lastMsg.Content[0].Text
				if memCtx := r.memory.RetrieveContext(ctx, query, 5); memCtx != "" {
					task.SystemPrompt += memCtx
					needsRebuild = true
					log.Info("long-term memories injected into context")
				}
			}
		}

		if needsRebuild {
			messages = buildMessages(task)
		}
	}

	// Outer loop: handles follow-up messages from the same conversation.
	for {
		assistantMsg, err := r.innerLoop(ctx, task, messages, steerCh)
		if err != nil {
			return err
		}
		messages = append(messages, assistantMsg)

		// Accumulate text content from the assistant's final message for the
		// task result that gets published on agent_end.
		assistantText := extractText(assistantMsg)
		accumulatedContent += assistantText

		// Persist the exchange to session history.
		if r.memory != nil {
			// Append the latest user message.
			if len(task.Messages) > 0 {
				lastUser := task.Messages[len(task.Messages)-1]
				if lastUser.Role == "user" && len(lastUser.Content) > 0 {
					_ = r.memory.AppendMessage(ctx, "user", lastUser.Content[0].Text)
				}
			}
			// Append the assistant reply.
			if assistantText != "" {
				_ = r.memory.AppendMessage(ctx, "assistant", assistantText)
			}
		}

		// Non-blocking check for a follow-up task on the same channel.
		select {
		case followUpData, ok := <-taskCh:
			if !ok {
				return nil
			}
			var followUp Task
			if err := json.Unmarshal(followUpData, &followUp); err != nil {
				log.Warn("failed to parse follow-up, finishing turn", "error", err)
				return nil
			}
			// Only treat same-conversation messages as follow-ups.
			if followUp.ConversationID != task.ConversationID {
				log.Warn("follow-up has different conv_id, ignoring", "follow_up_conv_id", followUp.ConversationID)
				return nil
			}
			log.Info("follow-up received, re-entering inner loop")
			// Append the new user messages from the follow-up (convert transport → proto).
			newMsgs := toProtoMessages(followUp.Messages[len(task.Messages):])
			messages = append(messages, newMsgs...)
			task = &followUp
		case <-time.After(2 * time.Second):
			// No follow-up within window, done.
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// innerLoop runs one LLM turn: drain steering → stream LLM → execute tools → repeat.
// Returns the final assistant message when the LLM stops calling tools.
func (r *Runtime) innerLoop(
	ctx context.Context,
	task *Task,
	messages []*volundv1.LLMMessage,
	steerCh <-chan stream.SteerMessage,
) (*volundv1.LLMMessage, error) {
	turnID := newID()

	r.stream.Publish(task.ConversationID, stream.Event{
		Type:   stream.EventTurnStart,
		TurnID: turnID,
	})

	for round := 0; round < task.MaxToolRounds; round++ {
		// 1. Drain any steering messages (mid-run user corrections).
		messages = drainSteering(messages, steerCh)

		// 2. Apply token budget before calling the LLM.
		messages = trimMessages(messages, r.cfg.ContextBudget)

		// 3. Stream LLM call.
		textContent, toolCalls, stopReason, usage, err := r.streamLLM(ctx, task, messages, turnID)
		if err != nil {
			return nil, fmt.Errorf("LLM call round %d: %w", round, err)
		}

		// Emit usage event after each LLM call.
		if usage != nil {
			r.emitUsage(ctx, task, usage)
		}

		// 4. Build assistant message from response.
		assistantMsg := buildAssistantMessage(textContent, toolCalls)

		// 5. No tool calls → final answer, turn is done.
		if len(toolCalls) == 0 {
			r.stream.Publish(task.ConversationID, stream.Event{
				Type:       stream.EventTurnEnd,
				TurnID:     turnID,
				StopReason: stopReason,
			})
			return assistantMsg, nil
		}

		// 6. Execute each tool call, collect results.
		messages = append(messages, assistantMsg)
		toolResultMsg := r.executeTools(ctx, task.ConversationID, turnID, toolCalls)
		messages = append(messages, toolResultMsg)
	}

	return nil, ErrMaxRoundsExceeded
}

// streamLLM calls the LLM router with streaming, publishes delta events,
// and collects the full response (text + any tool calls).
// llmUsage holds token counts from an LLM response.
type llmUsage struct {
	InputTokens  int
	OutputTokens int
}

func (r *Runtime) streamLLM(
	ctx context.Context,
	task *Task,
	messages []*volundv1.LLMMessage,
	turnID string,
) (text string, calls []tools.Call, stopReason string, usage *llmUsage, err error) {
	if r.llm == nil {
		return "", nil, "", nil, fmt.Errorf("LLM client not connected")
	}

	toolDefs := r.toolRegistry.Definitions()
	toolNames := make([]string, len(toolDefs))
	for i, td := range toolDefs {
		toolNames[i] = td.Name
	}
	slog.Info("calling LLM", "round", len(messages), "tools", len(toolDefs), "tool_names", toolNames, "provider", task.Provider, "model", task.Model)

	streamClient, err := r.llm.StreamChat(ctx, &volundv1.StreamChatRequest{
		TenantId:       task.TenantID,
		AgentId:        task.AgentID,
		Provider:       task.Provider,
		Model:          task.Model,
		Messages:       messages,
		Tools:          toolDefs,
		MaxTokens:      task.MaxTokens,
		Temperature:    task.Temperature,
		ConversationId: task.ConversationID,
		RequestId:      turnID,
	})
	if err != nil {
		return "", nil, "", nil, fmt.Errorf("StreamChat: %w", err)
	}

	// toolCalls keyed by tool_use_id to handle streamed vs batched tool events.
	toolCallMap := make(map[string]*tools.Call)

	for {
		resp, recvErr := streamClient.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return text, nil, "", nil, fmt.Errorf("stream recv: %w", recvErr)
		}

		switch v := resp.Chunk.(type) {
		case *volundv1.StreamChatResponse_TextDelta:
			text += v.TextDelta
			r.stream.Publish(task.ConversationID, stream.Event{
				Type:    stream.EventDelta,
				TurnID:  turnID,
				Content: v.TextDelta,
			})

		case *volundv1.StreamChatResponse_ToolUse:
			tu := v.ToolUse
			toolCallMap[tu.Id] = &tools.Call{
				ID:        tu.Id,
				Name:      tu.Name,
				InputJSON: tu.InputJson,
			}

		case *volundv1.StreamChatResponse_Complete:
			stopReason = v.Complete.GetStopReason()
			// Capture token usage from the complete message.
			if u := v.Complete.GetUsage(); u != nil {
				usage = &llmUsage{
					InputTokens:  int(u.InputTokens),
					OutputTokens: int(u.OutputTokens),
				}
			}
			// Some providers batch tool calls in the complete message.
			for _, block := range v.Complete.GetContent() {
				if tu := block.GetToolUse(); tu != nil {
					if _, exists := toolCallMap[tu.Id]; !exists {
						toolCallMap[tu.Id] = &tools.Call{
							ID:        tu.Id,
							Name:      tu.Name,
							InputJSON: tu.InputJson,
						}
					}
				}
			}
		}
	}

	calls = make([]tools.Call, 0, len(toolCallMap))
	for _, tc := range toolCallMap {
		calls = append(calls, *tc)
	}
	return text, calls, stopReason, usage, nil
}

// executeTools runs all tool calls, emits tool_start/tool_end events, and
// returns a single "tool" role message containing all results.
func (r *Runtime) executeTools(
	ctx context.Context,
	convID string,
	turnID string,
	calls []tools.Call,
) *volundv1.LLMMessage {
	resultBlocks := make([]*volundv1.ContentBlock, 0, len(calls))

	for _, tc := range calls {
		argsJSON, _ := json.Marshal(tc.InputJSON)
		r.stream.Publish(convID, stream.Event{
			Type:     stream.EventToolStart,
			TurnID:   turnID,
			ToolName: tc.Name,
			Args:     string(argsJSON),
		})

		result := r.toolRegistry.Execute(ctx, tc)

		r.stream.Publish(convID, stream.Event{
			Type:     stream.EventToolEnd,
			TurnID:   turnID,
			ToolName: tc.Name,
			Result:   result.Content,
			IsError:  result.IsError,
		})

		resultBlocks = append(resultBlocks, &volundv1.ContentBlock{
			Block: &volundv1.ContentBlock_ToolResult{
				ToolResult: &volundv1.ToolResultContent{
					ToolUseId: result.CallID,
					Content:   safety.SanitizeToolResult(result.Content),
					IsError:   result.IsError,
				},
			},
		})
	}

	return &volundv1.LLMMessage{
		Role:    "tool",
		Content: resultBlocks,
	}
}

// buildMessages converts the task's transport messages to proto and prepends the system prompt.
func buildMessages(task *Task) []*volundv1.LLMMessage {
	protoMsgs := toProtoMessages(task.Messages)
	if task.SystemPrompt == "" {
		return protoMsgs
	}
	sys := &volundv1.LLMMessage{
		Role: "system",
		Content: []*volundv1.ContentBlock{
			{Block: &volundv1.ContentBlock_Text{Text: task.SystemPrompt}},
		},
	}
	msgs := make([]*volundv1.LLMMessage, 0, len(protoMsgs)+1)
	return append(append(msgs, sys), protoMsgs...)
}

// buildAssistantMessage constructs an assistant LLMMessage from streamed content.
func buildAssistantMessage(text string, calls []tools.Call) *volundv1.LLMMessage {
	var blocks []*volundv1.ContentBlock
	if text != "" {
		blocks = append(blocks, &volundv1.ContentBlock{
			Block: &volundv1.ContentBlock_Text{Text: text},
		})
	}
	for _, tc := range calls {
		blocks = append(blocks, &volundv1.ContentBlock{
			Block: &volundv1.ContentBlock_ToolUse{
				ToolUse: &volundv1.ToolUseContent{
					Id:        tc.ID,
					Name:      tc.Name,
					InputJson: tc.InputJSON,
				},
			},
		})
	}
	return &volundv1.LLMMessage{Role: "assistant", Content: blocks}
}

// drainSteering pulls all pending steering messages and injects them as user turns.
func drainSteering(messages []*volundv1.LLMMessage, steerCh <-chan stream.SteerMessage) []*volundv1.LLMMessage {
	for {
		select {
		case steer, ok := <-steerCh:
			if !ok {
				return messages
			}
			slog.Info("steering message injected")
			messages = append(messages, &volundv1.LLMMessage{
				Role: "user",
				Content: []*volundv1.ContentBlock{
					{Block: &volundv1.ContentBlock_Text{Text: steer.Content}},
				},
			})
		default:
			return messages
		}
	}
}

// extractText returns the concatenated text content from an LLM message.
func extractText(msg *volundv1.LLMMessage) string {
	if msg == nil {
		return ""
	}
	var text string
	for _, block := range msg.Content {
		if tb, ok := block.Block.(*volundv1.ContentBlock_Text); ok {
			text += tb.Text
		}
	}
	return text
}

// newID generates a short random hex ID for turn tracking.
func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
