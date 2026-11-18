package chat

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ToolHandler executes a registered tool with the JSON arguments supplied by
// the model. A returned error stops the current reply; recoverable tool errors
// can instead be returned as the result with a nil error.
type ToolHandler func(context.Context, string) (string, error)

// Tool describes an optional capability that can be registered with a chat
// provider. Registration is programmatic; it does not add user-facing
// configuration.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Handler     ToolHandler
	// ScheduledOnly keeps a runtime tool out of ordinary model requests. The
	// provider also checks this boundary when dispatching a forged call.
	ScheduledOnly bool
	// ScheduledParameters narrows a tool schema for a genuine scheduled turn
	// while keeping the same handler available for allowed management actions.
	ScheduledParameters map[string]any
}

// ToolRegistrar is the small seam skills can use to provide model tools
// without depending on a concrete provider implementation.
type ToolRegistrar interface {
	RegisterTool(Tool) error
}

type registeredTool struct {
	definition          codexTool
	scheduledParameters map[string]any
	handler             ToolHandler
	scheduledOnly       bool
}

type toolRegistry struct {
	mu     sync.RWMutex
	tools  []registeredTool
	byName map[string]ToolHandler
}

func (r *toolRegistry) registerTool(tool Tool) error {
	return r.registerVisibility(codexTool{
		Type:        "function",
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  tool.Parameters,
	}, tool.Handler, tool.ScheduledOnly, tool.ScheduledParameters)
}

func (r *toolRegistry) register(definition codexTool, handler ToolHandler) error {
	return r.registerVisibility(definition, handler, false, nil)
}

func (r *toolRegistry) registerVisibility(definition codexTool, handler ToolHandler, scheduledOnly bool, scheduledParameters map[string]any) error {
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	if definition.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if definition.Description == "" {
		return fmt.Errorf("tool %q description is required", definition.Name)
	}
	if definition.Parameters == nil {
		return fmt.Errorf("tool %q parameters are required", definition.Name)
	}
	if handler == nil {
		return fmt.Errorf("tool %q handler is required", definition.Name)
	}
	if definition.Type == "" {
		definition.Type = "function"
	}
	if definition.Type != "function" {
		return fmt.Errorf("tool %q has unsupported type %q", definition.Name, definition.Type)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName == nil {
		r.byName = make(map[string]ToolHandler)
	}
	if _, exists := r.byName[definition.Name]; exists {
		return fmt.Errorf("tool %q is already registered", definition.Name)
	}
	r.tools = append(r.tools, registeredTool{
		definition:          definition,
		scheduledParameters: scheduledParameters,
		handler:             handler,
		scheduledOnly:       scheduledOnly,
	})
	r.byName[definition.Name] = handler
	return nil
}

func (r *toolRegistry) definitions() []codexTool {
	return r.definitionsFor(context.Background())
}

func (r *toolRegistry) definitionsFor(ctx context.Context) []codexTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	scheduled := genuineScheduledCronContext(ctx)
	definitions := make([]codexTool, 0, len(r.tools))
	for _, tool := range r.tools {
		if tool.scheduledOnly && !scheduled {
			continue
		}
		definition := tool.definition
		if scheduled && tool.scheduledParameters != nil {
			definition.Parameters = tool.scheduledParameters
		}
		definitions = append(definitions, definition)
	}
	return definitions
}

func (r *toolRegistry) handler(name string) (ToolHandler, bool) {
	return r.handlerFor(name, context.Background())
}

func (r *toolRegistry) handlerFor(name string, ctx context.Context) (ToolHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	scheduled := genuineScheduledCronContext(ctx)
	for _, tool := range r.tools {
		if tool.definition.Name != name || tool.scheduledOnly && !scheduled {
			continue
		}
		handler, ok := r.byName[name]
		return handler, ok
	}
	return nil, false
}
