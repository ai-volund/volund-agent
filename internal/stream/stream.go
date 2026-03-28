// Package stream handles lightweight NATS pub/sub for real-time agent events.
// This is separate from the CloudEvents emitter — this package owns the hot path
// for LLM token streaming, task dispatch, and mid-run steering.
//
// NATS subject layout:
//
//	volund.conv.{convId}.stream       — agent publishes events; gateway subscribes
//	volund.agent.{instanceId}.task    — control plane publishes task payloads
//	volund.agent.{instanceId}.steer   — gateway publishes mid-run user corrections
package stream

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go"
)

// Stream manages NATS connections for real-time agent messaging.
type Stream struct {
	conn     *nats.Conn
	noop     bool
	mu       sync.Mutex
	taskSub  *nats.Subscription
	steerSub *nats.Subscription
}

// Event is a lightweight JSON event published to volund.conv.{convId}.stream.
// No CloudEvents envelope — the consumer is always the gateway WebSocket bridge.
type Event struct {
	Type        string `json:"type"`
	TurnID      string `json:"turn_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
	ConvID      string `json:"conv_id,omitempty"`
	ProfileType string `json:"profile_type,omitempty"`
	Content     string `json:"content,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	Args        string `json:"args,omitempty"`
	Result      string `json:"result,omitempty"`
	IsError     bool   `json:"is_error,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
	Message     string `json:"message,omitempty"`
	Fatal       bool   `json:"fatal,omitempty"`
}

// SteerMessage is received on the steer channel (mid-run user correction).
type SteerMessage struct {
	Content string `json:"content"`
}

// Event type constants.
const (
	EventAgentStart = "agent_start"
	EventTurnStart  = "turn_start"
	EventDelta      = "delta"
	EventToolStart  = "tool_start"
	EventToolUpdate = "tool_update"
	EventToolEnd    = "tool_end"
	EventTurnEnd    = "turn_end"
	EventAgentEnd   = "agent_end"
	EventError      = "error"
)

// Connect creates a Stream connected to the given NATS server.
// If natsURL is empty a no-op stream is returned (useful for local testing).
func Connect(natsURL string) (*Stream, error) {
	if natsURL == "" {
		slog.Info("no NATS URL configured, stream is no-op")
		return &Stream{noop: true}, nil
	}
	conn, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("nats stream: connect to %s: %w", natsURL, err)
	}
	slog.Info("stream connected to NATS", "url", natsURL)
	return &Stream{conn: conn}, nil
}

// Publish sends an Event to the conversation stream subject.
// Fire-and-forget: errors are logged but not returned.
func (s *Stream) Publish(convID string, evt Event) {
	if s.noop {
		slog.Debug("stream no-op, discarding event", "type", evt.Type, "conv_id", convID)
		return
	}
	data, err := json.Marshal(evt)
	if err != nil {
		slog.Warn("stream: failed to marshal event", "type", evt.Type, "error", err)
		return
	}
	subject := "volund.conv." + convID + ".stream"
	if err := s.conn.Publish(subject, data); err != nil {
		slog.Warn("stream: failed to publish event", "subject", subject, "error", err)
	}
}

// SubscribeTask subscribes to the task dispatch channel for this instance.
// Returns a channel that receives raw JSON task payloads.
func (s *Stream) SubscribeTask(instanceID string) (<-chan []byte, error) {
	ch := make(chan []byte, 16)
	if s.noop {
		// Return a channel that never closes — runtime blocks on ctx.Done().
		return ch, nil
	}
	subject := "volund.agent." + instanceID + ".task"
	sub, err := s.conn.Subscribe(subject, func(msg *nats.Msg) {
		data := make([]byte, len(msg.Data))
		copy(data, msg.Data)
		select {
		case ch <- data:
		default:
			slog.Warn("stream: task channel full, dropping message", "subject", subject)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("nats stream: subscribe task %s: %w", subject, err)
	}
	s.mu.Lock()
	s.taskSub = sub
	s.mu.Unlock()
	slog.Info("stream subscribed to task channel", "subject", subject)
	return ch, nil
}

// SubscribePool subscribes to the agent pool queue group for the given profile name.
// NATS delivers each published task to exactly one subscriber in the group,
// implementing load-balanced task dispatch without a DB-based claim step.
func (s *Stream) SubscribePool(profileName string, taskCh chan<- []byte) error {
	if s.noop {
		return nil
	}
	subject := "volund.pool." + profileName
	_, err := s.conn.QueueSubscribe(subject, subject, func(msg *nats.Msg) {
		data := make([]byte, len(msg.Data))
		copy(data, msg.Data)
		select {
		case taskCh <- data:
		default:
			slog.Warn("stream: pool task channel full, dropping message", "subject", subject)
		}
	})
	if err != nil {
		return fmt.Errorf("nats stream: queue subscribe pool %s: %w", subject, err)
	}
	slog.Info("stream subscribed to pool queue group", "subject", subject)
	return nil
}

// SubscribeSteer subscribes to the steering channel for this instance.
// Returns a channel that receives parsed SteerMessages.
func (s *Stream) SubscribeSteer(instanceID string) (<-chan SteerMessage, error) {
	ch := make(chan SteerMessage, 32)
	if s.noop {
		return ch, nil
	}
	subject := "volund.agent." + instanceID + ".steer"
	sub, err := s.conn.Subscribe(subject, func(msg *nats.Msg) {
		var steer SteerMessage
		if err := json.Unmarshal(msg.Data, &steer); err != nil {
			slog.Warn("stream: failed to parse steer message", "error", err)
			return
		}
		select {
		case ch <- steer:
		default:
			slog.Warn("stream: steer channel full, dropping message", "subject", subject)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("nats stream: subscribe steer %s: %w", subject, err)
	}
	s.mu.Lock()
	s.steerSub = sub
	s.mu.Unlock()
	slog.Info("stream subscribed to steer channel", "subject", subject)
	return ch, nil
}

// PublishTask publishes a raw task payload to a specialist pool subject.
// The NATS queue group on the other end ensures only one specialist picks it up.
func (s *Stream) PublishTask(profileName string, data []byte) error {
	if s.noop {
		slog.Debug("stream no-op, discarding task publish", "profile", profileName)
		return nil
	}
	subject := "volund.pool." + profileName
	if err := s.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publishing task to %s: %w", subject, err)
	}
	return nil
}

// SubscribeTaskResult subscribes to the result subject for a specific task.
// Returns a channel that receives at most one result payload, and a cleanup
// function that must be called when the result has been received or the
// subscription is no longer needed.
func (s *Stream) SubscribeTaskResult(taskID string) (<-chan []byte, func(), error) {
	ch := make(chan []byte, 1)
	if s.noop {
		return ch, func() {}, nil
	}
	subject := "volund.task." + taskID + ".result"
	sub, err := s.conn.Subscribe(subject, func(msg *nats.Msg) {
		data := make([]byte, len(msg.Data))
		copy(data, msg.Data)
		select {
		case ch <- data:
		default:
			// Already have a result buffered; drop duplicate.
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("subscribing to task result %s: %w", subject, err)
	}
	slog.Debug("subscribed to task result", "subject", subject)
	return ch, func() { _ = sub.Unsubscribe() }, nil
}

// PublishTaskResult publishes a specialist's result to the task result subject.
// The orchestrator subscribes to this subject to populate its inbox.
func (s *Stream) PublishTaskResult(taskID string, data []byte) error {
	if s.noop {
		slog.Debug("stream no-op, discarding task result publish", "task_id", taskID)
		return nil
	}
	subject := "volund.task." + taskID + ".result"
	if err := s.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publishing task result to %s: %w", subject, err)
	}
	return nil
}

// Close drains subscriptions and closes the NATS connection.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.taskSub != nil {
		_ = s.taskSub.Unsubscribe()
	}
	if s.steerSub != nil {
		_ = s.steerSub.Unsubscribe()
	}
	if s.conn != nil {
		s.conn.Drain() //nolint:errcheck
	}
	return nil
}
