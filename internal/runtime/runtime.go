package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ai-volund/volund-agent/internal/config"
	"github.com/ai-volund/volund-agent/internal/events"
	"github.com/ai-volund/volund-agent/internal/llm"
	"github.com/ai-volund/volund-agent/internal/memory"
)

const heartbeatInterval = 30 * time.Second

// Runtime is the core agent runtime that orchestrates all subsystems.
type Runtime struct {
	cfg     *config.Config
	emitter *events.Emitter
	llm     *llm.Client
	memory  memory.Manager
	inbox   *Inbox
}

// New creates a new Runtime with the given configuration.
func New(cfg *config.Config) *Runtime {
	return &Runtime{
		cfg:    cfg,
		inbox:  NewInbox(),
		memory: memory.NewNoopManager(),
	}
}

// Start runs the agent runtime main loop. It blocks until the context is
// cancelled or a fatal error occurs.
func (r *Runtime) Start(ctx context.Context) error {
	// 1. Load agent profile.
	slog.Info("loading agent profile",
		"profile", r.cfg.ProfileName,
		"type", r.cfg.ProfileType,
	)

	// 2. Connect to NATS (optional, graceful failure if unavailable).
	emitter, err := events.NewEmitter(r.cfg.NATSUrl)
	if err != nil {
		slog.Warn("NATS unavailable, running without event emission", "error", err)
	} else {
		r.emitter = emitter
		slog.Info("connected to NATS", "url", r.cfg.NATSUrl)
	}

	// 3. Connect to LLM Router (optional, graceful failure if unavailable).
	llmClient, err := llm.NewClient(r.cfg.LLMRouterAddr)
	if err != nil {
		slog.Warn("LLM Router unavailable, running without LLM access", "error", err)
	} else {
		r.llm = llmClient
		slog.Info("connected to LLM Router", "addr", r.cfg.LLMRouterAddr)
	}

	// 4. Start heartbeat goroutine.
	go r.heartbeatLoop(ctx)

	slog.Info("agent runtime started, entering main loop")

	// 5. Main message loop — blocks on context cancellation for now.
	<-ctx.Done()

	slog.Info("context cancelled, exiting main loop")
	return nil
}

// Stop performs graceful shutdown of all runtime resources.
func (r *Runtime) Stop() error {
	slog.Info("stopping agent runtime")

	var errs []error

	if r.llm != nil {
		if err := r.llm.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing LLM client: %w", err))
		}
	}

	if r.emitter != nil {
		if err := r.emitter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing event emitter: %w", err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	slog.Info("agent runtime stopped cleanly")
	return nil
}

func (r *Runtime) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.emitHeartbeat(ctx)
		}
	}
}

func (r *Runtime) emitHeartbeat(ctx context.Context) {
	if r.emitter == nil {
		slog.Debug("skipping heartbeat, no emitter configured")
		return
	}

	data := map[string]string{
		"agent_id":     r.cfg.AgentID,
		"tenant_id":    r.cfg.TenantID,
		"profile":      r.cfg.ProfileName,
		"profile_type": r.cfg.ProfileType,
		"status":       "alive",
	}

	if err := r.emitter.Emit(ctx, "volund.agent.heartbeat", data); err != nil {
		slog.Warn("failed to emit heartbeat", "error", err)
	} else {
		slog.Debug("heartbeat emitted")
	}
}
