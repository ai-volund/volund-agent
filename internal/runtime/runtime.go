package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ai-volund/volund-agent/internal/config"
	"github.com/ai-volund/volund-agent/internal/events"
	"github.com/ai-volund/volund-agent/internal/llm"
	"github.com/ai-volund/volund-agent/internal/memory"
	votel "github.com/ai-volund/volund-agent/internal/otel"
	"github.com/ai-volund/volund-agent/internal/safety"
	"github.com/ai-volund/volund-agent/internal/skill"
	"github.com/ai-volund/volund-agent/internal/stream"
	"github.com/ai-volund/volund-agent/internal/tools"
	"github.com/ai-volund/volund-agent/internal/tools/builtin"
)

var tracer = votel.Tracer("volund-agent/runtime")

const heartbeatInterval = 30 * time.Second

// inboxAdapter wraps *Inbox to satisfy builtin.TaskInbox, breaking the import
// cycle between runtime and builtin.
type inboxAdapter struct {
	inbox *Inbox
}

func (a *inboxAdapter) Get(taskID string) *builtin.TaskResultEntry {
	r := a.inbox.Get(taskID)
	if r == nil {
		return nil
	}
	return &builtin.TaskResultEntry{
		TaskID:  r.TaskID,
		Status:  r.Status,
		Content: r.Content,
		Error:   r.Error,
	}
}

// Runtime is the core agent runtime — connects to NATS and LLM router,
// subscribes to task/steer channels, and drives the two-tier processing loop.
type Runtime struct {
	cfg          *config.Config
	emitter      *events.Emitter
	llm          *llm.Client
	memory       memory.Manager
	stream       *stream.Stream
	toolRegistry *tools.Registry
	inbox        *Inbox // orchestrator-only: stores specialist task results

	// skillClients holds MCP connections opened during skill loading.
	// They are stopped on shutdown.
	skillClients []skill.ToolCaller

	// currentDelegationDepth is set from the incoming task and used by
	// makeDispatchFunc to propagate depth to sub-tasks.
	currentDelegationDepth int
	currentTaskID          string
}

// New creates a Runtime with the given config. Call Start to begin processing.
func New(cfg *config.Config) *Runtime {
	mgr := initMemory(cfg)
	return &Runtime{
		cfg:    cfg,
		memory: mgr,
	}
}

// initMemory creates the best available memory manager based on config.
func initMemory(cfg *config.Config) memory.Manager {
	if cfg.RedisAddr == "" {
		slog.Info("memory: no Redis configured, using noop session store")
		return memory.NewNoopManager()
	}

	// Build long-term store (calls gateway memory API).
	var longTerm memory.LongTermStore
	if cfg.GatewayURL != "" {
		longTerm = memory.NewAPIStore(cfg.GatewayURL, cfg.TenantID, "", "")
	}

	mgr, err := memory.NewRedisManager(
		memory.RedisConfig{Addr: cfg.RedisAddr},
		"", // convID set per-task
		longTerm,
	)
	if err != nil {
		slog.Warn("memory: Redis unavailable, falling back to noop", "error", err)
		return memory.NewNoopManager()
	}
	slog.Info("memory: Redis session store connected", "addr", cfg.RedisAddr)
	return mgr
}

// Start connects subsystems, subscribes to NATS channels, and runs the main
// task processing loop. Blocks until ctx is cancelled.
func (r *Runtime) Start(ctx context.Context) error {
	slog.Info("loading agent profile",
		"profile", r.cfg.ProfileName,
		"type", r.cfg.ProfileType,
		"instance_id", r.cfg.InstanceID,
	)

	// Connect to NATS stream (task dispatch + steering + conv event publishing).
	s, err := stream.Connect(r.cfg.NATSUrl)
	if err != nil {
		slog.Warn("NATS unavailable, using no-op stream", "error", err)
		s, _ = stream.Connect("") // guaranteed no-op on empty URL
	}
	r.stream = s

	// Connect to LLM Router.
	llmClient, err := llm.NewClient(r.cfg.LLMRouterAddr)
	if err != nil {
		slog.Warn("LLM Router unavailable", "addr", r.cfg.LLMRouterAddr, "error", err)
	} else {
		r.llm = llmClient
		slog.Info("connected to LLM Router", "addr", r.cfg.LLMRouterAddr)
	}

	// Connect CloudEvents emitter (lifecycle events: heartbeat, agent.started, etc.).
	emitter, err := events.NewEmitter(r.cfg.NATSUrl)
	if err != nil {
		slog.Warn("CloudEvents emitter unavailable", "error", err)
	} else {
		r.emitter = emitter
	}

	// Build tool registry based on profile type.
	r.toolRegistry = r.buildToolRegistry()

	// Subscribe to task channels:
	// 1. Direct instance channel — targeted dispatch (e.g. follow-up to same pod).
	// 2. Pool queue group — load-balanced dispatch by profile name.
	// Both deliver to the same channel; NATS ensures each task goes to one pod.
	taskCh, err := r.stream.SubscribeTask(r.cfg.InstanceID)
	if err != nil {
		return fmt.Errorf("subscribing to task channel: %w", err)
	}
	// SubscribePool writes to a bidirectional channel; wrap the receive-only taskCh.
	poolCh := make(chan []byte, 16)
	if err := r.stream.SubscribePool(r.cfg.ProfileName, poolCh); err != nil {
		slog.Warn("pool subscription failed, only direct dispatch available", "error", err)
	}

	steerCh, err := r.stream.SubscribeSteer(r.cfg.InstanceID)
	if err != nil {
		return fmt.Errorf("subscribing to steer channel: %w", err)
	}

	go r.heartbeatLoop(ctx)

	slog.Info("agent runtime ready, waiting for tasks")

	// Main loop: receive tasks from direct instance channel or pool queue group.
	// processTask may pick up follow-ups from taskCh (outer loop) before returning.
	for {
		var taskData []byte
		var ok bool
		select {
		case <-ctx.Done():
			return nil
		case taskData, ok = <-taskCh:
		case taskData, ok = <-poolCh:
		}

		if !ok {
			slog.Info("task channel closed, shutting down")
			return nil
		}
		var task Task
		if err := json.Unmarshal(taskData, &task); err != nil {
			slog.Error("failed to parse task payload", "error", err)
			continue
		}

		// Extract distributed trace context from the task (injected by gateway dispatcher).
		taskCtx := votel.ExtractContext(ctx, task.TraceContext)
		taskCtx, span := tracer.Start(taskCtx, "agent.processTask",
			trace.WithAttributes(
				attribute.String("task_id", task.TaskID),
				attribute.String("conv_id", task.ConversationID),
				attribute.String("profile", task.ProfileName),
			),
		)
		// Track delegation depth for sub-task dispatching.
		r.currentDelegationDepth = task.DelegationDepth
		r.currentTaskID = task.TaskID

		if err := r.processTask(taskCtx, &task, taskCh, steerCh); err != nil {
			span.RecordError(err)
			slog.ErrorContext(taskCtx, "task failed", "task_id", task.TaskID, "conv_id", task.ConversationID, "error", err)
			r.stream.Publish(task.ConversationID, stream.Event{
				Type:    stream.EventError,
				Message: err.Error(),
				Fatal:   false,
			})
		}
		span.End()
	}
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
	if r.stream != nil {
		if err := r.stream.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing stream: %w", err))
		}
	}
	if r.emitter != nil {
		if err := r.emitter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing emitter: %w", err))
		}
	}
	for _, c := range r.skillClients {
		c.Stop()
	}
	r.skillClients = nil

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}
	slog.Info("agent runtime stopped cleanly")
	return nil
}

// buildToolRegistry registers built-in tools appropriate for this profile type.
func (r *Runtime) buildToolRegistry() *tools.Registry {
	reg := tools.NewRegistry()

	// All agents get code execution — sandboxed when configured, subprocess fallback.
	sandbox := builtin.NewSandboxExecutor(builtin.SandboxConfig{
		Endpoint:  r.cfg.SandboxEndpoint,
		RouterURL: r.cfg.SandboxRouterURL,
		TenantID:  r.cfg.SandboxTenantID,
		PoolRef:   r.cfg.SandboxPoolRef,
	})
	if sandbox != nil {
		if sandbox.IsRouterMode() {
			slog.Info("sandbox executor enabled (router mode)", "router_url", r.cfg.SandboxRouterURL)
		} else {
			slog.Info("sandbox executor enabled (direct mode)", "endpoint", r.cfg.SandboxEndpoint)
		}
		reg.Register(builtin.NewRunCodeSandboxed(sandbox))
	} else {
		reg.Register(builtin.RunCode{})
	}
	reg.Register(&builtin.WebSearch{})
	reg.Register(builtin.NewReadMemory(r.memory))
	reg.Register(builtin.NewWriteMemory(r.memory))

	// Orchestrators additionally get task delegation tools.
	if r.cfg.ProfileType == "orchestrator" {
		r.inbox = NewInbox()
		reg.Register(builtin.NewCreateTask(r.makeDispatchFunc()))
		reg.Register(builtin.NewGetTaskResult(&inboxAdapter{inbox: r.inbox}))
	}

	// Default security hooks — applied to ALL tool executions regardless of
	// profile type. Order matters: validation before execution, redaction after.
	reg.AddBeforeHook(safety.ToolArgumentValidationHook)
	reg.AddAfterHook(safety.SecretRedactionHook)
	reg.AddAfterHook(safety.OutputSizeLimitHook(safety.MaxContentLength))

	slog.Info("tool registry built",
		"profile_type", r.cfg.ProfileType,
		"tools", len(reg.Definitions()),
	)
	return reg
}

// loadSkills resolves skill specs from a task, appending prompt skills to the
// system prompt and registering MCP/CLI tools. Returns the extended system prompt.
func (r *Runtime) loadSkills(ctx context.Context, task *Task) string {
	if len(task.Skills) == 0 {
		return task.SystemPrompt
	}

	result, err := skill.Load(ctx, task.Skills)
	if err != nil {
		slog.Warn("skill loading failed", "error", err)
		return task.SystemPrompt
	}

	// Register MCP/CLI tools.
	for _, t := range result.Tools {
		if r.toolRegistry.Has(t.Name()) {
			slog.Warn("skill tool name conflicts with builtin, skipping", "name", t.Name())
			continue
		}
		// Wrap skill.Tool as tools.Tool via the adapter.
		r.toolRegistry.Register(&skillToolAdapter{inner: t})
	}

	// Track clients for shutdown.
	r.skillClients = append(r.skillClients, result.Clients...)

	slog.Info("skills loaded",
		"prompt_skills", len(task.Skills),
		"mcp_tools", len(result.Tools),
	)

	// Append prompt extensions to system prompt.
	if result.PromptExtensions != "" {
		return task.SystemPrompt + result.PromptExtensions
	}
	return task.SystemPrompt
}

// applyDefaults fills zero-value task fields from agent config.
func (r *Runtime) applyDefaults(task *Task) {
	if task.SystemPrompt == "" {
		task.SystemPrompt = r.cfg.SystemPrompt
	}
	if task.Provider == "" {
		task.Provider = r.cfg.Provider
	}
	if task.Model == "" {
		task.Model = r.cfg.Model
	}
	if task.MaxTokens == 0 {
		task.MaxTokens = r.cfg.MaxTokens
	}
	if task.Temperature == 0 {
		task.Temperature = 0.7
	}
	if task.MaxToolRounds == 0 {
		task.MaxToolRounds = r.cfg.MaxToolRounds
	}
	if task.AgentID == "" {
		task.AgentID = r.cfg.AgentID
	}
	if task.TenantID == "" {
		task.TenantID = r.cfg.TenantID
	}
}

// delegationTimeout is the max time to wait for a specialist result.
const delegationTimeout = 120 * time.Second

// makeDispatchFunc returns a DispatchFunc that publishes sub-tasks to a
// specialist pool via NATS and starts a background goroutine to watch for the
// result. The task is marked as pending in the orchestrator's inbox immediately.
func (r *Runtime) makeDispatchFunc() builtin.DispatchFunc {
	return func(ctx context.Context, profileName, taskDescription string, extra map[string]any) (string, error) {
		// Cycle detection: check delegation depth before dispatching.
		nextDepth := r.currentDelegationDepth + 1
		if nextDepth > 5 {
			return "", fmt.Errorf("delegation depth %d exceeds maximum (5) — possible delegation cycle", nextDepth)
		}

		taskID := newID()

		// Build a Task payload for the specialist.
		subTask := Task{
			TaskID:          taskID,
			ProfileName:     profileName,
			ProfileType:     "specialist",
			TenantID:        r.cfg.TenantID,
			AgentID:         r.cfg.AgentID,
			ParentTaskID:    r.currentTaskID,
			DelegationDepth: nextDepth,
			Messages: []Message{
				{
					Role: "user",
					Content: []Block{
						{Type: "text", Text: taskDescription},
					},
				},
			},
		}
		if extra != nil {
			// Encode extra context as a second message block so the specialist sees it.
			extraJSON, err := json.Marshal(extra)
			if err == nil && len(extraJSON) > 2 { // skip empty "{}"
				subTask.Messages[0].Content = append(subTask.Messages[0].Content,
					Block{Type: "text", Text: "Additional context: " + string(extraJSON)},
				)
			}
		}

		data, err := json.Marshal(subTask)
		if err != nil {
			return "", fmt.Errorf("marshaling sub-task: %w", err)
		}

		// Mark pending in inbox before publishing so the orchestrator can
		// immediately poll with get_task_result.
		r.inbox.SetPending(taskID)

		if err := r.stream.PublishTask(profileName, data); err != nil {
			return "", fmt.Errorf("publishing task to pool %s: %w", profileName, err)
		}

		// Start a background watcher that subscribes to the task result subject
		// and stores the result in the inbox when it arrives.
		go r.watchTaskResult(ctx, taskID)

		slog.Info("sub-task dispatched",
			"task_id", taskID,
			"profile", profileName,
			"delegation_depth", nextDepth,
			"parent_task_id", r.currentTaskID,
		)
		return taskID, nil
	}
}

// watchTaskResult subscribes to the NATS result subject for a dispatched task
// and writes the result into the inbox when it arrives. Blocks until the result
// is received, the delegation timeout expires, or ctx is cancelled.
func (r *Runtime) watchTaskResult(ctx context.Context, taskID string) {
	ch, cleanup, err := r.stream.SubscribeTaskResult(taskID)
	if err != nil {
		slog.Warn("failed to subscribe to task result", "task_id", taskID, "error", err)
		r.inbox.Put(&TaskResult{
			TaskID: taskID,
			Status: "error",
			Error:  "failed to subscribe to result channel: " + err.Error(),
		})
		return
	}
	defer cleanup()

	timer := time.NewTimer(delegationTimeout)
	defer timer.Stop()

	select {
	case data := <-ch:
		var result TaskResult
		if err := json.Unmarshal(data, &result); err != nil {
			slog.Warn("failed to parse task result", "task_id", taskID, "error", err)
			r.inbox.Put(&TaskResult{
				TaskID: taskID,
				Status: "error",
				Error:  "failed to parse result: " + err.Error(),
			})
			return
		}
		result.TaskID = taskID // ensure consistency
		r.inbox.Put(&result)
		slog.Info("task result received", "task_id", taskID, "status", result.Status)

	case <-timer.C:
		slog.Warn("delegation timeout exceeded", "task_id", taskID, "timeout", delegationTimeout)
		r.inbox.Put(&TaskResult{
			TaskID: taskID,
			Status: "error",
			Error:  fmt.Sprintf("specialist did not respond within %v", delegationTimeout),
		})

	case <-ctx.Done():
		slog.Debug("task result watcher cancelled", "task_id", taskID)
	}
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

// emitUsage publishes a token usage CloudEvent for billing/tracking.
func (r *Runtime) emitUsage(ctx context.Context, task *Task, usage *llmUsage) {
	if r.emitter == nil {
		return
	}
	if err := r.emitter.EmitUsage(ctx, &events.UsageData{
		TenantID:       task.TenantID,
		ConversationID: task.ConversationID,
		TaskID:         task.TaskID,
		InstanceID:     r.cfg.InstanceID,
		Provider:       task.Provider,
		Model:          task.Model,
		InputTokens:    usage.InputTokens,
		OutputTokens:   usage.OutputTokens,
	}); err != nil {
		slog.WarnContext(ctx, "failed to emit usage event", "error", err)
	}
}

func (r *Runtime) emitHeartbeat(ctx context.Context) {
	if r.emitter == nil {
		return
	}
	data := map[string]string{
		"agent_id":     r.cfg.AgentID,
		"instance_id":  r.cfg.InstanceID,
		"tenant_id":    r.cfg.TenantID,
		"profile":      r.cfg.ProfileName,
		"profile_type": r.cfg.ProfileType,
		"status":       "alive",
	}
	if err := r.emitter.Emit(ctx, "io.volund.agent.heartbeat", data); err != nil {
		slog.Warn("failed to emit heartbeat", "error", err)
	}
}
