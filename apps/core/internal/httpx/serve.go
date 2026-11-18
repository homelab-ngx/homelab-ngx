package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// shutdownGrace is how long in-flight requests get to drain on shutdown.
const shutdownGrace = 10 * time.Second

// Serve listens on the unix socket sockPath (mode 0660, any stale socket
// removed) and serves h until ctx is cancelled, then drains in-flight
// requests for up to 10 s. It returns nil on a clean shutdown.
//
// WriteTimeout is deliberately 0: a stack update or a subvolume usage scan
// can take minutes. ReadHeaderTimeout and IdleTimeout still bound slow or
// idle clients.
func Serve(ctx context.Context, sockPath string, h http.Handler, log *slog.Logger) error {
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	// Let other members of the socket's group (caddy) connect.
	if err := os.Chmod(sockPath, 0o660); err != nil {
		log.Warn("chmod socket failed", "socket", sockPath, "error", err)
	}
	return ServeListener(ctx, ln, h, log)
}

// ServeListener serves h on ln until ctx is cancelled, with the same timeouts
// and drain as Serve. The stack listener uses it with a TCP listener.
func ServeListener(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {

	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       120 * time.Second,
	}

	// stop ends the shutdown goroutine when Serve fails on its own; done lets
	// us wait for the drain before returning, because srv.Serve returns the
	// moment Shutdown starts, not when it finishes.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case <-stop:
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("API server shutdown error", "error", err)
		}
	}()

	log.Info("API listening", "address", ln.Addr().String())
	err := srv.Serve(ln)
	close(stop)
	<-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
