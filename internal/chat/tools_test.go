package chat

import (
	"context"
	"strings"
	"testing"
)

func TestCodexProviderRejectsInvalidOrDuplicateTools(t *testing.T) {
	provider := NewCodexProvider(CredentialStore{})
	valid := Tool{
		Name:        "skill_echo",
		Description: "Return a value from a test skill.",
		Parameters:  map[string]any{"type": "object"},
		Handler:     func(context.Context, string) (string, error) { return "", nil },
	}

	for _, tool := range []Tool{
		valid,
		{Name: "", Description: valid.Description, Parameters: valid.Parameters, Handler: valid.Handler},
		{Name: "missing-description", Parameters: valid.Parameters, Handler: valid.Handler},
		{Name: "missing-parameters", Description: valid.Description, Handler: valid.Handler},
		{Name: "missing-handler", Description: valid.Description, Parameters: valid.Parameters},
	} {
		if tool.Name == valid.Name {
			if err := provider.RegisterTool(tool); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := provider.RegisterTool(tool); err == nil {
			t.Fatalf("RegisterTool(%q) succeeded", tool.Name)
		}
	}

	if err := provider.RegisterTool(valid); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate tool error = %v", err)
	}
	if err := provider.RegisterTool(Tool{
		Name:        shellExecuteCodexTool.Name,
		Description: "replacement",
		Parameters:  map[string]any{"type": "object"},
		Handler:     valid.Handler,
	}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("built-in duplicate error = %v", err)
	}
}
