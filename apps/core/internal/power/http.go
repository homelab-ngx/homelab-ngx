package power

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the power operations.
type HTTP struct{ svc *Service }

// NewHTTP wraps the service.
func NewHTTP(s *Service) *HTTP { return &HTTP{svc: s} }

type statusOut struct{ Body PowerStatus }
type requestIn struct{ Body PowerRequest }

// Register adds the power operations: the GET is read scope, the rest
// operator-only. The stack API has none of them.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"system"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getPower", Method: http.MethodGet, Path: "/system/power", Tags: tags,
		Summary:     "The reboot or poweroff under way, and one scheduled.",
		Description: "While a shutdown runs, the steps show each stack stopping and then each pool and partition unmounting. A page waiting through a reboot polls this until boot_id changes.",
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		return &statusOut{Body: h.svc.Status(ctx)}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "requestPower", Method: http.MethodPost, Path: "/system/power", Tags: tags,
		Summary:       "Reboot or power off the host.",
		Description:   "Stops the running stacks, unmounts the pools and partitions, then reboots or powers off. Nothing is recorded: the next boot starts the same stacks and mounts the same pools. Refused while a stack operation, an update or a backup runs, unless force. Follow it in getPower.",
		DefaultStatus: http.StatusAccepted,
		Errors:        []int{http.StatusBadRequest, http.StatusConflict},
	}, func(ctx context.Context, in *requestIn) (*statusOut, error) {
		if err := h.svc.Request(ctx, in.Body.Action, in.Body.Force); err != nil {
			return nil, err
		}
		return &statusOut{Body: h.svc.Status(ctx)}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "cancelScheduledPower", Method: http.MethodDelete, Path: "/system/power/scheduled", Tags: tags,
		Summary: "Call off the scheduled reboot or poweroff.",
		Errors:  []int{http.StatusNotFound},
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		if err := h.svc.CancelScheduled(ctx); err != nil {
			return nil, err
		}
		return &statusOut{Body: h.svc.Status(ctx)}, nil
	})
}
