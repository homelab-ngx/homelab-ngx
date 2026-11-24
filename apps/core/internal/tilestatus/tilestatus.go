// Package tilestatus is the System Settings grid's badges: each feature
// reports the state of its tiles, and GET /settings/status collects them.
package tilestatus

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// TileState is a tile's tone. The empty State is no tone: a bare count, or no
// badge at all.
type TileState string

const (
	None TileState = ""
	OK   TileState = "ok"
	Warn TileState = "warn"
	Bad  TileState = "bad"
	Info TileState = "info"
	// Off dims the tile: the feature is turned off or not set up.
	Off TileState = "off"
)

// Schema documents TileState as its enum.
func (TileState) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{string(OK), string(Warn), string(Bad), string(Info), string(Off)}}
}

// TileStatus is one tile's badge.
type TileStatus struct {
	State TileState `json:"state,omitempty" doc:"ok, warn, bad or info: the badge's tone. off: the feature is off, and the tile is dimmed. Absent: no tone."`
	Count *int      `json:"count,omitempty" doc:"A number on the badge, such as a VPN's devices."`
}

// Of is a status with a tone and no count.
func Of(s TileState) TileStatus { return TileStatus{State: s} }

// Counted is a status with a tone and a count.
func Counted(s TileState, n int) TileStatus { return TileStatus{State: s, Count: &n} }

// Reporter gives the statuses of one feature's tiles, keyed by tile. A
// tile it leaves out, or a TileStatus with neither state nor count, has no badge.
type Reporter func(context.Context) (map[string]TileStatus, error)

// Timeout is how long a reporter may take before its tiles go without a
// badge.
const Timeout = 2 * time.Second

// Registry is the reporters, each named for its log lines.
type Registry struct {
	reporters []named
	timeout   time.Duration
	log       *slog.Logger
}

type named struct {
	name string
	r    Reporter
}

// NewRegistry is an empty registry; log may be nil.
func NewRegistry(log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Registry{timeout: Timeout, log: log}
}

// Add registers a feature's reporter.
func (g *Registry) Add(name string, r Reporter) { g.reporters = append(g.reporters, named{name, r}) }

// Collect runs every reporter at once, each with its own deadline. A
// reporter that fails, panics or runs out of time gives no badges; the rest
// are unaffected. A tile two reporters both name keeps the first's.
func (g *Registry) Collect(ctx context.Context) map[string]TileStatus {
	results := make([]map[string]TileStatus, len(g.reporters))
	var wg sync.WaitGroup
	for i, n := range g.reporters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = g.run(ctx, n)
		}()
	}
	wg.Wait()
	out := map[string]TileStatus{}
	for _, res := range results {
		for k, s := range res {
			if _, taken := out[k]; taken || (s.State == None && s.Count == nil) {
				continue
			}
			out[k] = s
		}
	}
	return out
}

// run runs one reporter; it returns as soon as the deadline passes, and the
// reporter's late answer is dropped.
func (g *Registry) run(ctx context.Context, n named) map[string]TileStatus {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	type answer struct {
		m   map[string]TileStatus
		err error
	}
	ch := make(chan answer, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- answer{err: fmt.Errorf("panic: %v", p)}
			}
		}()
		m, err := n.r(ctx)
		ch <- answer{m, err}
	}()
	select {
	case a := <-ch:
		if a.err != nil {
			g.log.Warn("tile status", "feature", n.name, "err", a.err)
			return nil
		}
		return a.m
	case <-ctx.Done():
		g.log.Warn("tile status", "feature", n.name, "err", ctx.Err())
		return nil
	}
}
