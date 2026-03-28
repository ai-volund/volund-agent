package runtime

import (
	"context"
	"time"
)

// InboxMessage represents a message received by the agent.
type InboxMessage struct {
	// Type identifies the kind of message (e.g. "task.assigned", "chat.message").
	Type string
	// Payload is the raw message content.
	Payload []byte
	// Timestamp is when the message was received.
	Timestamp time.Time
}

// Inbox manages incoming messages for the agent runtime.
type Inbox struct{}

// NewInbox creates a new Inbox.
func NewInbox() *Inbox {
	return &Inbox{}
}

// Drain retrieves all pending messages from the inbox. This is a stub that
// returns an empty slice until a real message transport is connected.
func (i *Inbox) Drain(_ context.Context) ([]InboxMessage, error) {
	return nil, nil
}
