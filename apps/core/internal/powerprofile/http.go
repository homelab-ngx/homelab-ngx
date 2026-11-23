package powerprofile

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the power profile operations.
type HTTP struct{ svc *Service }

// NewHTTP wraps the service.
func NewHTTP(s *Service) *HTTP { return &HTTP{svc: s} }

type statusOut struct{ Body PowerProfileStatus }
type setIn struct{ Body PowerProfileSettings }

// Register adds the power profile operations: the GET is read scope, the
// PUT operator-only.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"system"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getPowerProfile", Method: http.MethodGet, Path: "/system/power-profile", Tags: tags,
		Summary:     "The power profile, how deep the host idles, and what keeps it awake.",
		Description: "idle covers the last minute (since boot just after the daemon starts). tunables say what the profile wrote on this host and what the kernel refused. blockers are the devices that keep the CPU package out of a deeper state, each with the profile that turns it off.",
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		st, err := h.svc.Status(ctx)
		if err != nil {
			return nil, err
		}
		return &statusOut{Body: st}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "setPowerProfile", Method: http.MethodPut, Path: "/system/power-profile", Tags: tags,
		Summary:     "Choose the power profile and the CPU governor.",
		Description: "Applied at once and again at every start. power-saver turns on ASPM on every PCIe link and USB autosuspend on every device; balanced and performance put back what the host booted with. `409 managed_by_nixos` on a NixOS host.",
		Errors:      []int{http.StatusBadRequest, http.StatusConflict},
	}, func(ctx context.Context, in *setIn) (*statusOut, error) {
		st, err := h.svc.Set(ctx, in.Body)
		switch {
		case errors.Is(err, ErrInvalid):
			return nil, httpx.BadRequest("invalid_setting", "%s", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
		case errors.Is(err, ErrManaged):
			return nil, httpx.Conflict("managed_by_nixos", "%s", err)
		case err != nil:
			return nil, err
		}
		return &statusOut{Body: st}, nil
	})
}
