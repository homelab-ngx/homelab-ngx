package services

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the services slice of the API.
type HTTP struct{ svc *Service }

// NewHTTP returns the HTTP adapter for svc.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

type listOut struct{ Body []ServiceInfo }

type unitIn struct {
	Unit string `path:"unit" doc:"Unit name; name and name.service both resolve."`
}

// Register adds the services operations: the lists are read, start, stop
// and restart of a stack's unit are control.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"services"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "listServices", Method: http.MethodGet, Path: "/services", Tags: tags,
		Summary: "The service units inside stacks, with their runtime state.",
	}, h.listServices)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "listCoreServices", Method: http.MethodGet, Path: "/core-services", Tags: tags,
		Summary: "Critical systemd-owned units on the host, read-only.",
	}, h.listCoreServices)
	for _, op := range []struct{ id, action, summary string }{
		{"startService", "start", "Start a service unit inside a stack."},
		{"stopService", "stop", "Stop a service unit inside a stack."},
		{"restartService", "restart", "Restart a service unit inside a stack."},
	} {
		action := op.action
		httpx.Register(a, httpx.ScopeControl, huma.Operation{
			OperationID: op.id, Method: http.MethodPost, Path: "/services/{unit}/" + action, Tags: tags,
			Summary: op.summary, DefaultStatus: http.StatusNoContent,
			Errors: []int{http.StatusForbidden, http.StatusNotFound},
		}, func(ctx context.Context, in *unitIn) (*struct{}, error) {
			return nil, h.act(ctx, in.Unit, action)
		})
	}
}

func (h *HTTP) listServices(ctx context.Context, _ *struct{}) (*listOut, error) {
	return &listOut{Body: h.svc.List(ctx)}, nil
}

func (h *HTTP) listCoreServices(ctx context.Context, _ *struct{}) (*listOut, error) {
	return &listOut{Body: h.svc.Core(ctx)}, nil
}

// act runs a unit action. A stack (on the stack listener) may act on stack
// units only: anything else, a core unit or a name nobody knows, is 403
// core_unit, so a stack learns nothing about the host's units.
func (h *HTTP) act(ctx context.Context, unit, action string) error {
	if stack, ok := httpx.CallerStack(ctx); ok && !h.svc.inStack(ctx, unit) {
		return httpx.Forbidden("core_unit", "stack %s may control stack units only, not %q", stack, unit)
	}
	return problem(h.svc.act(ctx, unit, action))
}

// problem maps a domain error to a problem: 404 unknown_unit, 500
// systemd_failed for a failed systemctl call (its message is the useful part
// and is sent as written). Anything else passes through and becomes a
// generic 500.
func problem(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnknownUnit) {
		return httpx.NotFound("unknown_unit", "%s", err)
	}
	var ce *cmdrunner.Error
	if errors.As(err, &ce) {
		return httpx.Internal("systemd_failed", "%s", err)
	}
	return err
}
