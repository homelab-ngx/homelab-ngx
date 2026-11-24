// Package logging is the single source of slog loggers for homelab-daemon
// and homelab-dash. Every Go package in those binaries MUST obtain its
// logger via Logger(module) — direct slog.Default()/slog.Info() calls are
// forbidden and CI-linted.
//
// Field contract:
//
//	module: required, low-cardinality, one of the canonical names listed
//	        in logging.md.
//	kind:   "event" by default; CmdLogger() returns "cmd" for shell
//	        invocations emitted by cmdrunner.
package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// canonicalModules is the allowed module-name set. Add new modules here.
var canonicalModules = map[string]struct{}{
	// daemon
	"bug": {}, "api": {}, "api-token": {}, "secrets": {}, "monitor": {}, "middleware": {},
	"storage": {}, "updates": {}, "cmdrunner": {},
	"notifier": {}, "store": {}, "config": {}, "httpx": {},
	"services": {}, "backups": {}, "systemd": {}, "sysupdates": {},
	"system": {}, "notifications": {}, "doctor": {}, "stacks": {}, "drafts": {}, "llm": {}, "metrics": {},
	"hostcfg": {}, "installer": {}, "auth": {}, "power": {}, "powerprofile": {}, "powermeter": {}, "tilestatus": {},
	// dash
	"dash_server": {}, "dash_daemon": {}, "dash_storage": {}, "dash_bug": {},
}

var (
	once    sync.Once
	rootLog *slog.Logger
)

// Init installs a JSON handler on slog.Default() once. Safe to call from
// multiple init() blocks. The daemon's main and dash's main should also
// call it explicitly before any goroutine logs.
func Init() {
	once.Do(func() {
		h := slog.NewJSONHandler(syslogPrefix{os.Stdout}, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		rootLog = slog.New(h)
		slog.SetDefault(rootLog)
	})
}

// Logger returns a slog.Logger pre-tagged with module=<m>, kind=event. A
// module outside canonicalModules still gets its logger, with a warning: a
// panic here crashed the daemon at every start on the host (2026-11-17), and
// the cmd/daemon test checks the names before a deploy instead.
func Logger(m string) *slog.Logger {
	l := lazy("module", m, "kind", "event")
	if !Known(m) {
		l.Warn("logging: module not in canonicalModules; add it to internal/logging/logging.go", "unknown_module", m)
	}
	return l
}

// Known reports whether m is an allowed module name, for tests that check
// the names a program logs under before it runs.
func Known(m string) bool {
	_, ok := canonicalModules[m]
	return ok
}

// CmdLogger returns a logger tagged with module=cmdrunner, kind=cmd.
// Only cmdrunner.Run() should use this. Callers of cmdrunner keep using
// Logger("<their module>") for the surrounding event log.
func CmdLogger() *slog.Logger {
	return lazy("module", "cmdrunner", "kind", "cmd")
}

// lazy returns a logger that follows slog.Default() rather than binding the
// handler that exists when it is created. Package-level loggers are built
// during package init, before main picks a destination (Init: stdout for the
// daemon, InitCLI: stderr for the CLI so --json stdout stays clean); binding
// them eagerly would pin whichever handler happened to exist first.
func lazy(args ...any) *slog.Logger {
	return slog.New(&lazyHandler{}).With(args...)
}

type lazyHandler struct {
	attrs []slog.Attr
	group string
	cache atomic.Pointer[resolvedHandler]
}

type resolvedHandler struct {
	from *slog.Logger
	h    slog.Handler
}

func (h *lazyHandler) resolve() slog.Handler {
	def := slog.Default()
	if c := h.cache.Load(); c != nil && c.from == def {
		return c.h
	}
	out := def.Handler()
	if len(h.attrs) > 0 {
		out = out.WithAttrs(h.attrs)
	}
	if h.group != "" {
		out = out.WithGroup(h.group)
	}
	h.cache.Store(&resolvedHandler{from: def, h: out})
	return out
}

func (h *lazyHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.resolve().Enabled(ctx, l)
}

func (h *lazyHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.resolve().Handle(ctx, r)
}

func (h *lazyHandler) WithAttrs(as []slog.Attr) slog.Handler {
	if h.group != "" {
		// Attrs after a group belong inside it; resolve eagerly for this rare case.
		return h.resolve().WithAttrs(as)
	}
	return &lazyHandler{attrs: append(append([]slog.Attr{}, h.attrs...), as...)}
}

func (h *lazyHandler) WithGroup(name string) slog.Handler {
	if h.group != "" {
		return h.resolve().WithGroup(name)
	}
	return &lazyHandler{attrs: h.attrs, group: name}
}

// CtxKey is the exported context-key type. Defined here (and not in the
// daemon's middleware) so both pkg/logging.FromContext AND the daemon's
// middleware refer to the same key value — context.Value() lookups are
// keyed by interface identity, so a separate unexported type in
// middleware.go would not collide with this one and FromContext would
// silently see nil. ALL packages that read/write these keys must import
// this type.
type CtxKey int

const (
	CtxKeyReqID CtxKey = iota // string: request id propagated by HTTP middleware
)

// FromContext extracts a logger pre-tagged with the request id, if present.
// Used by API handlers so every line within a request shares a req_id.
// Falls back to the supplied base logger unchanged when no id is set.
func FromContext(ctx context.Context, base *slog.Logger) *slog.Logger {
	out := base
	if v := ctx.Value(CtxKeyReqID); v != nil {
		if s, ok := v.(string); ok && s != "" {
			out = out.With("req_id", s)
		}
	}
	return out
}

// InitCLI installs a stderr JSON handler for the CLI so stdout stays
// clean for machine-readable output. Level comes from HOMELAB_LOG_LEVEL
// (debug|info|warn|error), default WARN. Call it first in the CLI's main.
func InitCLI() {
	once.Do(func() {
		lvl := slog.LevelWarn
		switch strings.ToLower(os.Getenv("HOMELAB_LOG_LEVEL")) {
		case "debug":
			lvl = slog.LevelDebug
		case "info":
			lvl = slog.LevelInfo
		case "error":
			lvl = slog.LevelError
		}
		rootLog = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
		slog.SetDefault(rootLog)
	})
}

// syslogPrefix puts a syslog priority prefix ("<4>") before each JSON record,
// which journald strips and stores as the line's PRIORITY (SyslogLevelPrefix,
// on by default). Without it every daemon line lands at info, so
// `journalctl -p warning` and the Logs page level filter would miss them.
// slog's JSON handler writes each record in one Write, with "time" and then
// "level" first.
type syslogPrefix struct{ w io.Writer }

func (p syslogPrefix) Write(b []byte) (int, error) {
	head := b[:min(len(b), 96)]
	prefix := "<6>"
	switch {
	case bytes.Contains(head, []byte(`"level":"ERROR`)):
		prefix = "<3>"
	case bytes.Contains(head, []byte(`"level":"WARN`)):
		prefix = "<4>"
	case bytes.Contains(head, []byte(`"level":"DEBUG`)):
		prefix = "<7>"
	}
	if _, err := p.w.Write(append([]byte(prefix), b...)); err != nil {
		return 0, err
	}
	return len(b), nil
}
