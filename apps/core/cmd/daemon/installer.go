package main

import (
	"context"
	"log/slog"
	"net/http"
	"syscall"

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
	installer.NewHTTP(installer.New(cmdrunner.OS{}, installer.UDisks{}, logging.Logger("installer"))).Register(apis)
	return httpx.Serve(ctx, socket, httpx.Middleware(nil)(mux), log)
}
