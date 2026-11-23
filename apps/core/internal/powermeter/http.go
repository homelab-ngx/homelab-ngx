package powermeter

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the power meter operations.
type HTTP struct{ svc *Service }

// NewHTTP wraps the service.
func NewHTTP(s *Service) *HTTP { return &HTTP{svc: s} }

type statusOut struct{ Body PowerMeter }
type setIn struct{ Body PowerMeterSet }
type testOut struct{ Body PowerMeterTest }

// Register adds the power meter operations: the GET is read scope, the
// rest operator-only.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"system"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getPowerMeter", Method: http.MethodGet, Path: "/system/power-meter", Tags: tags,
		Summary:     "The power meter: its Home Assistant connection and its last reading.",
		Description: "The meter is a power sensor in Home Assistant, such as a smart plug the host is plugged into. The token is never returned.",
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		st, err := h.svc.Status(ctx)
		if err != nil {
			return nil, err
		}
		return &statusOut{Body: st}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "setPowerMeter", Method: http.MethodPut, Path: "/system/power-meter", Tags: tags,
		Summary:     "Set up, change or turn off the power meter.",
		Description: "The daemon reconnects when the address, entity or token changed. An empty url and entity turn the meter off and remove the token.",
		Errors:      []int{http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *setIn) (*statusOut, error) {
		st, err := h.svc.Set(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &statusOut{Body: st}, nil
	})
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "testPowerMeter", Method: http.MethodPost, Path: "/system/power-meter/test", Tags: tags,
		Summary:     "Connect to Home Assistant once and read the entity, without saving.",
		Description: "Without a token in the body the stored one is used. A failed connection is a 200 with ok false and a code.",
		Errors:      []int{http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *setIn) (*testOut, error) {
		t, err := h.svc.Test(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		return &testOut{Body: t}, nil
	})
}
