package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

// SandboxExecutor runs code in an isolated container pod via a sandbox API.
// The sandbox pod runs with the gVisor runtime class for kernel-level isolation.
//
// Architecture: the agent calls a sandbox service endpoint which manages
// ephemeral pods. The sandbox service runs in the cluster and exposes:
//   POST /execute  {language, code, timeout} → {stdout, stderr, exit_code}
type SandboxExecutor struct {
	// endpoint is the sandbox service URL (e.g., "http://sandbox-service:8090").
	endpoint string
	client   *http.Client
}

// SandboxConfig holds configuration for the sandbox executor.
type SandboxConfig struct {
	// Endpoint is the sandbox service URL. If empty, falls back to subprocess.
	Endpoint string
}

// NewSandboxExecutor creates a sandbox executor. Returns nil if endpoint is empty.
func NewSandboxExecutor(cfg SandboxConfig) *SandboxExecutor {
	if cfg.Endpoint == "" {
		return nil
	}
	return &SandboxExecutor{
		endpoint: cfg.Endpoint,
		client:   &http.Client{Timeout: 70 * time.Second}, // slightly longer than max code timeout
	}
}

type sandboxRequest struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Timeout  int    `json:"timeout"`
}

type sandboxResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// Execute sends code to the sandbox service for isolated execution.
func (s *SandboxExecutor) Execute(ctx context.Context, language, code string, timeout time.Duration) (string, error) {
	reqBody, _ := json.Marshal(sandboxRequest{
		Language: language,
		Code:     code,
		Timeout:  int(timeout.Seconds()),
	})

	req, err := http.NewRequestWithContext(ctx, "POST", s.endpoint+"/execute", bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create sandbox request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("sandbox request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sandbox error (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var result sandboxResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse sandbox response: %w", err)
	}

	if result.Error != "" {
		return "", fmt.Errorf("sandbox: %s", result.Error)
	}

	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += "stderr:\n" + result.Stderr
	}

	if result.ExitCode != 0 {
		return output, fmt.Errorf("execution failed (exit code %d)", result.ExitCode)
	}

	return output, nil
}

// RunCodeSandboxed is a version of RunCode that uses the sandbox executor when
// available, falling back to subprocess execution.
type RunCodeSandboxed struct {
	sandbox *SandboxExecutor
}

// NewRunCodeSandboxed creates a sandboxed code executor.
// If sandbox is nil, falls back to subprocess execution (same as RunCode).
func NewRunCodeSandboxed(sandbox *SandboxExecutor) *RunCodeSandboxed {
	return &RunCodeSandboxed{sandbox: sandbox}
}

func (r *RunCodeSandboxed) Name() string { return "run_code" }

func (r *RunCodeSandboxed) Definition() *volundv1.ToolDefinition {
	return (RunCode{}).Definition()
}

func (r *RunCodeSandboxed) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input runCodeInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid run_code input: %w", err)
	}

	timeout := defaultCodeTimeout
	if input.TimeoutSeconds > 0 {
		timeout = time.Duration(min(input.TimeoutSeconds, 60)) * time.Second
	}

	// Use sandbox if available.
	if r.sandbox != nil {
		slog.Debug("executing code in sandbox", "language", input.Language, "timeout", timeout)
		return r.sandbox.Execute(ctx, input.Language, input.Code, timeout)
	}

	// Fall back to subprocess execution.
	slog.Debug("executing code as subprocess (no sandbox configured)", "language", input.Language)
	return (RunCode{}).Execute(ctx, inputJSON)
}

// SandboxServiceEndpoint returns the sandbox endpoint from environment.
func SandboxServiceEndpoint() string {
	return os.Getenv("VOLUND_SANDBOX_ENDPOINT")
}
