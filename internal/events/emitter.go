package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	cloudevents "github.com/cloudevents/sdk-go/v2"
	"github.com/cloudevents/sdk-go/v2/event"
	"github.com/nats-io/nats.go"
)

// Emitter publishes CloudEvents to a NATS subject.
type Emitter struct {
	conn *nats.Conn
	noop bool
}

// NewEmitter connects to NATS at the given URL and returns an Emitter.
// If natsURL is empty, a no-op emitter is returned that discards all events.
func NewEmitter(natsURL string) (*Emitter, error) {
	if natsURL == "" {
		slog.Info("no NATS URL configured, using no-op emitter")
		return &Emitter{noop: true}, nil
	}

	conn, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS at %s: %w", natsURL, err)
	}

	return &Emitter{conn: conn}, nil
}

// Emit publishes a CloudEvent with the given type and data to NATS.
// The subject is derived from the event type (dots replaced with dots, used as-is).
func (e *Emitter) Emit(_ context.Context, eventType string, data interface{}) error {
	if e.noop {
		slog.Debug("no-op emitter, discarding event", "type", eventType)
		return nil
	}

	ce := event.New()
	ce.SetType(eventType)
	ce.SetSource("volund-agent")
	ce.SetID(cloudevents.NewEvent().ID())
	if err := ce.SetData(cloudevents.ApplicationJSON, data); err != nil {
		return fmt.Errorf("setting CloudEvent data: %w", err)
	}

	payload, err := json.Marshal(ce)
	if err != nil {
		return fmt.Errorf("marshaling CloudEvent: %w", err)
	}

	subject := "volund.events." + eventType
	if err := e.conn.Publish(subject, payload); err != nil {
		return fmt.Errorf("publishing event to %s: %w", subject, err)
	}

	return nil
}

// Close closes the underlying NATS connection.
func (e *Emitter) Close() error {
	if e.conn != nil {
		e.conn.Close()
	}
	return nil
}
