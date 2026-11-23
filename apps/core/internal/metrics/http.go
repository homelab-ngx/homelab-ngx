package metrics

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the metrics package's HTTP adapter.
type HTTP struct{ m *Metrics }

// NewHTTP returns the adapter for m. m may be nil when only the OpenAPI
// document is built.
func NewHTTP(m *Metrics) *HTTP { return &HTTP{m: m} }

type historyIn struct {
	Range MetricsRange `query:"range" required:"true" doc:"How far back: 24h at 1-minute points, 7d at 5 minutes, 30d at 30 minutes."`
}

type historyOut struct{ Body MetricsHistory }

type profilesIn struct {
	Range    MetricsRange `query:"range" required:"true" doc:"How far back."`
	IdleOnly bool         `query:"idle_only" doc:"Count only the minutes the host's CPU was under 5%."`
}

type profilesOut struct{ Body MetricsPowerProfiles }

// Register adds the metrics operations.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"metrics"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getMetricsHistory", Method: http.MethodGet, Path: "/metrics/history", Tags: tags,
		Summary: "Stored CPU, memory, pressure, disk, network and temperature of the host, core and each stack.",
		Description: "Averages of the stored minutes (the highest reading in `cpu_max` and " +
			"`memory_max`). Minutes are kept 30 days.",
		Errors: []int{http.StatusBadRequest},
	}, func(ctx context.Context, in *historyIn) (*historyOut, error) {
		f, err := h.m.History(ctx, in.Range)
		if err != nil {
			return nil, httpx.Internal("metrics_history", "read the metrics history: %v", err)
		}
		ch, err := h.m.ProfileChanges(ctx, in.Range)
		if err != nil {
			return nil, httpx.Internal("metrics_history", "read the power profile changes: %v", err)
		}
		return &historyOut{Body: MetricsHistory{Frames: f, ProfileChanges: ch}}, nil
	})
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getMetricsPowerProfiles", Method: http.MethodGet, Path: "/metrics/power-profiles", Tags: tags,
		Summary: "The host's average power under each power profile.",
		Description: "From the stored minutes, each of which records the power profile in force. `wall` is the power " +
			"meter's reading, `cpu` the CPU package's. Minutes from before the profile was recorded are left out.",
		Errors: []int{http.StatusBadRequest},
	}, func(ctx context.Context, in *profilesIn) (*profilesOut, error) {
		p, err := h.m.PowerProfiles(ctx, in.Range, in.IdleOnly)
		if err != nil {
			return nil, httpx.Internal("metrics_power_profiles", "read the power per profile: %v", err)
		}
		return &profilesOut{Body: p}, nil
	})
	httpx.RegisterSSE(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "streamMetrics", Method: http.MethodGet, Path: "/metrics/stream", Tags: tags,
		Summary: "Live metrics: a snapshot of the last 15 minutes, then a sample every 2 s.",
	}, map[string]any{"snapshot": MetricsSnapshot{}, "sample": MetricsSample{}}, func(ctx context.Context, _ *struct{}, send sse.Sender) {
		snap, samples, cancel := h.m.Subscribe()
		defer cancel()
		if send.Data(MetricsSnapshot{Info: h.m.Info(), Frames: snap}) != nil {
			return
		}
		step := int(Every.Seconds())
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-samples:
				if send.Data(MetricsSample{Frames: framesOf([]sample{s}, step, false)}) != nil {
					return
				}
			}
		}
	})
}
