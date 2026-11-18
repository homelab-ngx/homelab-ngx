package llm

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the assistant settings slice of the daemon API, the operator's
// alone.
type HTTP struct{ s *Settings }

// NewHTTP returns the adapter for s. s may be nil when only the OpenAPI
// document is built.
func NewHTTP(s *Settings) *HTTP { return &HTTP{s: s} }

type settingsBody struct{ Body AssistantSettings }

type setIn struct{ Body AssistantSettingsSet }

type testBody struct{ Body AssistantTest }

type modelsIn struct{ Body AssistantModelsQuery }

type modelsBody struct{ Body AssistantModels }

type limitsBody struct{ Body AssistantLimits }

// Register adds the operations.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"assistant"}
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getAssistantSettings", Method: http.MethodGet, Path: "/settings/assistant", Tags: tags,
		Summary: "The language model the stack editor asks for help, if any. The API key is never returned.",
	}, func(ctx context.Context, _ *struct{}) (*settingsBody, error) {
		s, err := h.s.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &settingsBody{Body: s}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "setAssistantSettings", Method: http.MethodPut, Path: "/settings/assistant", Tags: tags,
		Summary: "Set up the assistant, or turn it off with provider none.",
	}, func(ctx context.Context, in *setIn) (*settingsBody, error) {
		s, err := h.s.Set(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &settingsBody{Body: s}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "testAssistant", Method: http.MethodPost, Path: "/settings/assistant/test", Tags: tags,
		Summary: "Ask the model for one word, to prove the address, the key and the model.",
	}, func(ctx context.Context, _ *struct{}) (*testBody, error) {
		t, err := h.s.Test(ctx)
		if err != nil {
			return nil, err
		}
		return &testBody{Body: t}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "listAssistantModels", Method: http.MethodPost, Path: "/settings/assistant/models", Tags: tags,
		Summary: "List the models a server offers, before or after it is applied.",
	}, func(ctx context.Context, in *modelsIn) (*modelsBody, error) {
		m, err := h.s.Models(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &modelsBody{Body: m}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getAssistantLimits", Method: http.MethodGet, Path: "/settings/assistant/limits", Tags: tags,
		Summary: "How much of the applied provider's allowance is spent, when its server reports it (OpenCode Go).",
	}, func(ctx context.Context, _ *struct{}) (*limitsBody, error) {
		l, err := h.s.Limits(ctx)
		if err != nil {
			return nil, err
		}
		return &limitsBody{Body: l}, nil
	})
}
