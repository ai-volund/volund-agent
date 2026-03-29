package config

import (
	"os"
	"strconv"
)

// Config holds agent-specific configuration loaded from environment variables.
type Config struct {
	// InstanceID uniquely identifies this pod instance (assigned by control plane).
	InstanceID string
	// ProfileName is the agent profile name (e.g. "default-orchestrator").
	ProfileName string
	// ProfileType is either "orchestrator" or "specialist".
	ProfileType string
	// NATSUrl is the NATS server URL for event messaging.
	NATSUrl string
	// LLMRouterAddr is the gRPC address of the LLM Router service.
	LLMRouterAddr string
	// AgentID is a unique identifier for this agent instance.
	AgentID string
	// TenantID identifies the tenant this agent belongs to.
	TenantID string
	// SystemPrompt is the default system prompt for this agent profile.
	// Can be overridden per-task by the control plane.
	SystemPrompt string
	// Provider is the default LLM provider (e.g. "anthropic", "openai").
	Provider string
	// Model is the default LLM model (e.g. "claude-opus-4-5").
	Model string
	// MaxTokens is the default max tokens for LLM responses.
	MaxTokens int32
	// MaxToolRounds is the max number of tool call iterations per turn.
	MaxToolRounds int
	// ContextBudget is the max number of tokens (estimated) for the conversation
	// context window. Messages are trimmed to stay within this budget.
	ContextBudget int
	// RedisAddr is the Redis server address for session memory (host:port).
	RedisAddr string
	// GatewayURL is the URL of the Volund gateway for memory API calls.
	GatewayURL string
	// OpenTelemetry
	OTLPEndpoint string // VOLUND_OTLP_ENDPOINT — gRPC endpoint for OTLP collector
	Environment  string // VOLUND_ENV — deployment environment (dev, staging, prod)
	// Sandbox
	SandboxEndpoint  string // VOLUND_SANDBOX_ENDPOINT — sandbox service URL (v1 direct mode, empty = subprocess fallback)
	SandboxEnabled   bool   // VOLUND_SANDBOX_ENABLED — enable sandboxed code execution
	SandboxRouterURL string // VOLUND_SANDBOX_ROUTER_URL — sandbox router URL (v2 router mode, takes precedence over Endpoint)
	SandboxTenantID  string // VOLUND_SANDBOX_TENANT_ID — tenant for claim ownership (router mode)
	SandboxPoolRef   string // VOLUND_SANDBOX_POOL_REF — warm pool name (router mode, default "tool-execution-pool")
	// LogLevel controls structured logging verbosity (debug, info, warn, error).
	LogLevel string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		InstanceID:    envOrDefault("VOLUND_INSTANCE_ID", "instance-001"),
		ProfileName:   envOrDefault("VOLUND_PROFILE", "default"),
		ProfileType:   envOrDefault("VOLUND_PROFILE_TYPE", "specialist"),
		NATSUrl:       os.Getenv("VOLUND_NATS_URL"),
		LLMRouterAddr: envOrDefault("VOLUND_LLM_ROUTER_ADDR", "localhost:9090"),
		AgentID:       envOrDefault("VOLUND_AGENT_ID", "agent-001"),
		TenantID:      envOrDefault("VOLUND_TENANT_ID", "default-tenant"),
		SystemPrompt:  os.Getenv("VOLUND_SYSTEM_PROMPT"),
		Provider:      envOrDefault("VOLUND_PROVIDER", "anthropic"),
		Model:         envOrDefault("VOLUND_MODEL", "claude-opus-4-5"),
		MaxTokens:     int32(envOrDefaultInt("VOLUND_MAX_TOKENS", 8192)),
		MaxToolRounds: envOrDefaultInt("VOLUND_MAX_TOOL_ROUNDS", 10),
		ContextBudget: envOrDefaultInt("VOLUND_CONTEXT_BUDGET", 100000),
		RedisAddr:     os.Getenv("VOLUND_REDIS_ADDR"),
		OTLPEndpoint:  os.Getenv("VOLUND_OTLP_ENDPOINT"),
		Environment:   envOrDefault("VOLUND_ENV", "dev"),
		GatewayURL:      envOrDefault("VOLUND_GATEWAY_URL", "http://volund:8080"),
		SandboxEndpoint:  os.Getenv("VOLUND_SANDBOX_ENDPOINT"),
		SandboxEnabled:   os.Getenv("VOLUND_SANDBOX_ENABLED") == "true",
		SandboxRouterURL: os.Getenv("VOLUND_SANDBOX_ROUTER_URL"),
		SandboxTenantID:  os.Getenv("VOLUND_SANDBOX_TENANT_ID"),
		SandboxPoolRef:   envOrDefault("VOLUND_SANDBOX_POOL_REF", "tool-execution-pool"),
		LogLevel:        envOrDefault("VOLUND_LOG_LEVEL", "info"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
