package config

import "os"

// Config holds agent-specific configuration loaded from environment variables.
type Config struct {
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
	// LogLevel controls structured logging verbosity (debug, info, warn, error).
	LogLevel string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		ProfileName:   envOrDefault("VOLUND_PROFILE", "default"),
		ProfileType:   envOrDefault("VOLUND_PROFILE_TYPE", "specialist"),
		NATSUrl:       os.Getenv("VOLUND_NATS_URL"),
		LLMRouterAddr: envOrDefault("VOLUND_LLM_ROUTER_ADDR", "localhost:9090"),
		AgentID:       envOrDefault("VOLUND_AGENT_ID", "agent-001"),
		TenantID:      envOrDefault("VOLUND_TENANT_ID", "default-tenant"),
		LogLevel:      envOrDefault("VOLUND_LOG_LEVEL", "info"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
