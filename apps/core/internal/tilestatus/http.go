package tilestatus

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the tile status operation.
type HTTP struct{ reg *Registry }

// NewHTTP wraps the registry.
func NewHTTP(r *Registry) *HTTP { return &HTTP{reg: r} }

// SettingsStatus is every tile's badge.
type SettingsStatus struct {
	Tiles map[string]TileStatus `json:"tiles" doc:"Keyed by tile, such as wireguard or notifications. A tile that is absent has no badge."`
}

type statusOut struct{ Body SettingsStatus }

// Register adds GET /settings/status at read scope.
func (h *HTTP) Register(a httpx.APIs) {
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getSettingsStatus", Method: http.MethodGet, Path: "/settings/status", Tags: []string{"system"},
		Summary:     "The Settings tiles' badges.",
		Description: "Each feature reports its own tiles, in parallel and within a short deadline. A feature that fails or is too slow leaves its tiles without a badge; the call itself does not fail.",
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		return &statusOut{Body: SettingsStatus{Tiles: h.reg.Collect(ctx)}}, nil
	})
}
