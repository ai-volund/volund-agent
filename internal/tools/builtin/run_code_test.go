package builtin

import (
	"context"
	"strings"
	"testing"
)

func TestRunCode_Python_Success(t *testing.T) {
	rc := RunCode{}
	out, err := rc.Execute(context.Background(), `{"language":"python","code":"print('hello from python')"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "hello from python" {
		t.Fatalf("expected 'hello from python', got %q", out)
	}
}

func TestRunCode_Bash_Success(t *testing.T) {
	rc := RunCode{}
	out, err := rc.Execute(context.Background(), `{"language":"bash","code":"echo 'hello from bash'"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "hello from bash" {
		t.Fatalf("expected 'hello from bash', got %q", out)
	}
}

func TestRunCode_JavaScript_Success(t *testing.T) {
	rc := RunCode{}
	out, err := rc.Execute(context.Background(), `{"language":"javascript","code":"console.log('hello from js')"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "hello from js" {
		t.Fatalf("expected 'hello from js', got %q", out)
	}
}

func TestRunCode_InvalidLanguage(t *testing.T) {
	rc := RunCode{}
	_, err := rc.Execute(context.Background(), `{"language":"ruby","code":"puts 'hello'"}`)
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
	if !strings.Contains(err.Error(), "unsupported language") {
		t.Fatalf("expected 'unsupported language' in error, got %q", err.Error())
	}
}

func TestRunCode_InvalidJSON(t *testing.T) {
	rc := RunCode{}
	_, err := rc.Execute(context.Background(), `{bad json}`)
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
	if !strings.Contains(err.Error(), "invalid run_code input") {
		t.Fatalf("expected 'invalid run_code input' in error, got %q", err.Error())
	}
}

func TestRunCode_Timeout(t *testing.T) {
	rc := RunCode{}
	_, err := rc.Execute(context.Background(), `{"language":"bash","code":"sleep 5","timeout_seconds":1}`)
	if err == nil {
		t.Fatal("expected error for timed-out execution")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected 'timed out' in error, got %q", err.Error())
	}
}

func TestRunCode_Stderr(t *testing.T) {
	rc := RunCode{}
	out, err := rc.Execute(context.Background(), `{"language":"bash","code":"echo 'stdout line' && echo 'stderr line' >&2"}`)
	// bash writes to both stdout and stderr; the command itself succeeds
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "stdout line") {
		t.Fatalf("expected stdout content in output, got %q", out)
	}
	if !strings.Contains(out, "stderr") {
		t.Fatalf("expected stderr marker in output, got %q", out)
	}
	if !strings.Contains(out, "stderr line") {
		t.Fatalf("expected stderr content in output, got %q", out)
	}
}

func TestRunCode_Definition(t *testing.T) {
	rc := RunCode{}
	def := rc.Definition()

	if def.Name != "run_code" {
		t.Fatalf("expected name 'run_code', got %q", def.Name)
	}
	if def.Description == "" {
		t.Fatal("expected non-empty description")
	}
	if def.InputSchemaJson == "" {
		t.Fatal("expected non-empty input schema")
	}
	if !strings.Contains(def.InputSchemaJson, "language") {
		t.Fatal("expected schema to reference 'language'")
	}
	if !strings.Contains(def.InputSchemaJson, "code") {
		t.Fatal("expected schema to reference 'code'")
	}
}
