package main

import (
	"context"
	"log/slog"
	"net/http"
	"syscall"

	"golang.org/x/sync/errgroup"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/installer"
	"homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/system"
)

// runInstaller is the daemon on the installer stick: the installer's
// operations and the version on the socket, nothing else.
func runInstaller(ctx context.Context, socket string, log *slog.Logger) error {
	log.Info("installer mode: this is the installer stick")
	// The unit's UMask=0027 is for the daemon's own state; what the install
	// creates on the new disk (repart's /var/lib among it) must be readable
	// by the system's other users.
	syscall.Umask(0o022)
	version, _ := system.BuildInfo()
	mux := http.NewServeMux()
	apis := httpx.APIs{Operator: httpx.NewAPI(mux, "homelab-daemon", version)}
	system.RegisterVersion(apis) // the image's boot check waits for it
	inst := installer.New(cmdrunner.OS{}, installer.UDisks{}, logging.Logger("installer"))
	if err := inst.WriteCode(installer.CodeFile); err != nil {
		log.Warn("the take-over code is not on the console", "error", err)
	}
	// Temporary partitions an install left when the stick lost power.
	go inst.Sweep(ctx)
	installer.NewHTTP(inst).Register(apis)
	// The stick has no password: the installer's pages reach the same API
	// through Caddy on the web socket, ungated.
	h := httpx.Middleware(nil)(mux)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, socket, "", h, log) })
	g.Go(func() error { return httpx.Serve(gctx, httpx.WebSocketPath(socket), httpx.WebGroup, h, log) })
	return g.Wait()
}
