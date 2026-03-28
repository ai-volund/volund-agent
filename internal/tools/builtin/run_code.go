package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"
)

const defaultCodeTimeout = 10 * time.Second

// RunCode executes code as a subprocess in an isolated temp directory.
// v1: subprocess with timeout. v2 will swap in agent-sandbox + gVisor (see ADR-003).
type RunCode struct{}

type runCodeInput struct {
	Language      string `json:"language"`
	Code          string `json:"code"`
	TimeoutSeconds int   `json:"timeout_seconds,omitempty"`
}

func (RunCode) Name() string { return "run_code" }

func (RunCode) Definition() *volundv1.ToolDefinition {
	return &volundv1.ToolDefinition{
		Name:        "run_code",
		Description: "Execute code in a sandboxed subprocess. Returns stdout and stderr combined.",
		InputSchemaJson: `{
			"type": "object",
			"required": ["language", "code"],
			"properties": {
				"language": {
					"type": "string",
					"enum": ["python", "bash", "javascript"],
					"description": "Programming language to execute"
				},
				"code": {
					"type": "string",
					"description": "The code to execute"
				},
				"timeout_seconds": {
					"type": "integer",
					"description": "Execution timeout in seconds (max 60, default 10)",
					"minimum": 1,
					"maximum": 60
				}
			}
		}`,
	}
}

func (RunCode) Execute(ctx context.Context, inputJSON string) (string, error) {
	var input runCodeInput
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return "", fmt.Errorf("invalid run_code input: %w", err)
	}

	timeout := defaultCodeTimeout
	if input.TimeoutSeconds > 0 {
		timeout = time.Duration(min(input.TimeoutSeconds, 60)) * time.Second
	}

	workDir, err := os.MkdirTemp("", "volund-code-*")
	if err != nil {
		return "", fmt.Errorf("creating work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	switch input.Language {
	case "python":
		f := filepath.Join(workDir, "code.py")
		if err := os.WriteFile(f, []byte(input.Code), 0600); err != nil {
			return "", fmt.Errorf("writing code file: %w", err)
		}
		cmd = exec.CommandContext(execCtx, "python3", f)
	case "bash":
		f := filepath.Join(workDir, "code.sh")
		if err := os.WriteFile(f, []byte(input.Code), 0700); err != nil {
			return "", fmt.Errorf("writing code file: %w", err)
		}
		cmd = exec.CommandContext(execCtx, "bash", f)
	case "javascript":
		f := filepath.Join(workDir, "code.js")
		if err := os.WriteFile(f, []byte(input.Code), 0600); err != nil {
			return "", fmt.Errorf("writing code file: %w", err)
		}
		cmd = exec.CommandContext(execCtx, "node", f)
	default:
		return "", fmt.Errorf("unsupported language: %q", input.Language)
	}

	cmd.Dir = workDir
	// Restrict environment — only essential vars.
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + workDir,
		"TMPDIR=" + workDir,
	}
	applyResourceLimits(cmd)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()

	output := outBuf.String()
	if errBuf.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += "stderr:\n" + errBuf.String()
	}

	if runErr != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return output, fmt.Errorf("execution timed out after %v", timeout)
		}
		return output, fmt.Errorf("execution failed: %w", runErr)
	}

	return output, nil
}
