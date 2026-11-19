package ask

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
)

// HTTP is the ask adapter.
type HTTP struct{ svc *Service }

// NewHTTP wraps the service; nil registers the operation only (OpenAPI).
func NewHTTP(s *Service) *HTTP { return &HTTP{svc: s} }

type askIn struct{ Body AskRequest }

// Register adds the one operation, operator only: it sends what the host
// holds to the configured provider.
func (h *HTTP) Register(a httpx.APIs) {
	httpx.RegisterSSE(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "ask", Method: http.MethodPost, Path: "/ask", Tags: []string{"assistant"},
		Summary: "Ask the assistant about one thing the dashboard shows: log lines, a failed service, a backup run, " +
			"a stack update, a host update or a notification. The daemon reads the material itself. The answer " +
			"streams as text events, then one answer event; a failure is an error event.",
	}, map[string]any{"text": llm.AssistText{}, "thinking": llm.AssistThinking{}, "answer": AskAnswer{}, "error": llm.AssistError{}},
		func(ctx context.Context, in *askIn, send sse.Sender) {
			a, err := h.svc.Ask(ctx, in.Body, func(t string) {
				_ = send.Data(llm.AssistText{Text: t})
			}, func(t string) {
				_ = send.Data(llm.AssistThinking{Text: t})
			})
			if err != nil {
				msg := err.Error()
				var p *httpx.Problem
				if errors.As(err, &p) {
					msg = p.Detail
				}
				_ = send.Data(llm.AssistError{Message: msg})
				return
			}
			_ = send.Data(a)
		})
}
