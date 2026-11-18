package audit

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the security audit adapter.
type HTTP struct{ svc *Service }

// NewHTTP wraps the service; nil registers the operations only (OpenAPI).
func NewHTTP(s *Service) *HTTP { return &HTTP{svc: s} }

type auditBody struct{ Body SecurityAudit }

type listBody struct{ Body []SecurityAuditItem }

type idIn struct {
	ID int64 `path:"id" minimum:"1"`
}

// Register adds the operations, operator only: an audit sends what the host
// holds to the configured provider, and its report maps the host's weak
// spots.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"assistant"}
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "startSecurityAudit", Method: http.MethodPost, Path: "/security/audits", Tags: tags,
		DefaultStatus: http.StatusAccepted,
		Summary: "Start a security audit: the daemon gathers what it knows about the host in five areas and asks the " +
			"assistant's audit model about each in turn, then for a summary. It runs in the background and goes on " +
			"after a daemon restart; poll the audit for progress.",
	}, func(ctx context.Context, _ *struct{}) (*auditBody, error) {
		out, err := h.svc.Start(ctx)
		if err != nil {
			return nil, err
		}
		return &auditBody{Body: out}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "listSecurityAudits", Method: http.MethodGet, Path: "/security/audits", Tags: tags,
		Summary: "The newest security audits, newest first, with their progress and findings by severity.",
	}, func(ctx context.Context, _ *struct{}) (*listBody, error) {
		out, err := h.svc.List(ctx)
		if err != nil {
			return nil, err
		}
		return &listBody{Body: out}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getSecurityAudit", Method: http.MethodGet, Path: "/security/audits/{id}", Tags: tags,
		Summary: "One security audit in full: each area's findings and the material the model was given.",
	}, func(ctx context.Context, in *idIn) (*auditBody, error) {
		out, err := h.svc.Get(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &auditBody{Body: out}, nil
	})
}
