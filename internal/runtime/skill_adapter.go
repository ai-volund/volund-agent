package runtime

import (
	"context"

	volundv1 "github.com/ai-volund/volund-proto/gen/go/volund/v1"

	"github.com/ai-volund/volund-agent/internal/skill"
)

// skillToolAdapter wraps a skill.Tool so it satisfies tools.Tool.
// Both interfaces are identical but live in separate packages to avoid
// import cycles (skill → volund-proto, tools → volund-proto).
type skillToolAdapter struct {
	inner skill.Tool
}

func (a *skillToolAdapter) Name() string {
	return a.inner.Name()
}

func (a *skillToolAdapter) Definition() *volundv1.ToolDefinition {
	return a.inner.Definition()
}

func (a *skillToolAdapter) Execute(ctx context.Context, inputJSON string) (string, error) {
	return a.inner.Execute(ctx, inputJSON)
}
