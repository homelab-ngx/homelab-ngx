package system

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/httpx"
)

type versionOut struct{ Body Version }
type configOut struct{ Body config.ConfigView }
type logViewerOut struct{ Body LogViewerConfig }
type logsOut struct{ Body LogPage }

// RegisterVersion adds the version operation: every daemon has it, the
// installer stick's too (the image's boot check asks it).
func RegisterVersion(a httpx.APIs) {
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getVersion", Method: http.MethodGet, Path: "/version", Tags: []string{"system"},
		Summary: "Daemon build version.",
	}, func(context.Context, *struct{}) (*versionOut, error) {
		v, c := BuildInfo()
		return &versionOut{Body: Version{Version: v, Commit: c}}, nil
	})
}

// Register adds the system operations, all of them reads.
func (s *System) Register(a httpx.APIs) {
	tags := []string{"system"}
	RegisterVersion(a)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getConfig", Method: http.MethodGet, Path: "/config", Tags: tags,
		Summary: "Effective configuration with provenance.",
		Description: "The nix-declared configuration merged with runtime overrides. Each " +
			"overridable field says whether its value came from `nix` or `override`.",
		Errors: []int{http.StatusServiceUnavailable},
	}, func(context.Context, *struct{}) (*configOut, error) {
		v, err := s.Config()
		if err != nil {
			return nil, err
		}
		return &configOut{Body: v}, nil
	})
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getLogViewerConfig", Method: http.MethodGet, Path: "/log-viewer-config", Tags: tags,
		Summary: "Log viewer units generated from the core units and backups.",
		Errors:  []int{http.StatusServiceUnavailable},
	}, func(ctx context.Context, _ *struct{}) (*logViewerOut, error) {
		v, err := s.LogViewerConfig(ctx)
		if err != nil {
			return nil, err
		}
		return &logViewerOut{Body: v}, nil
	})
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getLogs", Method: http.MethodGet, Path: "/logs", Tags: tags,
		Summary: "Journal lines of one unit from the log viewer list, newest first.",
		Description: "Reads the host journal, or a stack's own journal for a stack unit " +
			"(running or not). `before` pages back from the oldest line shown; " +
			"`after` returns lines newer than the newest one shown. `more` is true " +
			"when lines were left out: older lines exist, or (with `after`) more " +
			"new lines arrived than `lines`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound},
	}, func(ctx context.Context, in *GetLogsParams) (*logsOut, error) {
		page, err := s.Logs(ctx, *in)
		if err != nil {
			return nil, err
		}
		return &logsOut{Body: page}, nil
	})
}
