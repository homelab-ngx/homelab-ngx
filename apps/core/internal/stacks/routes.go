package stacks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// routeSnippet is the Caddy config for a stack's routes. Caddy imports it
// inside the private domain's site, after the host's own routes: TLS and logging
// come from that site, a hostname the host already serves stays with the
// host, and a hostname outside the site never matches. Only validated
// manifest fields reach the file; a stack cannot write Caddy directives.
func routeSnippet(name, addr string, routes []Route) string {
	var b strings.Builder
	id := strings.ReplaceAll(name, "-", "_")
	for i, r := range routes {
		m := fmt.Sprintf("@stack_%s_%d", id, i)
		fmt.Fprintf(&b, "%s host %s\nhandle %s {\n", m, r.Host, m)
		if r.MaxBody != "" {
			fmt.Fprintf(&b, "\trequest_body {\n\t\tmax_size %s\n\t}\n", r.MaxBody)
		}
		fmt.Fprintf(&b, "\treverse_proxy %s:%d\n}\n", addr, r.Port)
	}
	return b.String()
}

func (s *Service) routeFile(name string) string {
	return filepath.Join(s.host.RoutesDir, name+".caddy")
}

// writeRoutes publishes the stack's routes and reloads Caddy. Routes that
// are already in place are left alone, so a daemon restart does not reload
// Caddy. A config Caddy refuses is taken back.
func (s *Service) writeRoutes(ctx context.Context, name, addr string, routes []Route) error {
	if len(routes) == 0 {
		return s.removeRoutes(ctx, name)
	}
	path, want := s.routeFile(name), routeSnippet(name, addr, routes)
	old, err := os.ReadFile(path)
	if err == nil && string(old) == want {
		return nil
	}
	had := err == nil
	if err := writeFile(path, want, 0o644); err != nil {
		return err
	}
	if err := s.reloadCaddy(ctx); err != nil {
		if had {
			_ = writeFile(path, string(old), 0o644)
		} else {
			_ = os.Remove(path)
		}
		return fmt.Errorf("caddy refused the routes: %w", err)
	}
	return nil
}

// removeRoutes takes the stack's routes out of Caddy.
func (s *Service) removeRoutes(ctx context.Context, name string) error {
	if err := os.Remove(s.routeFile(name)); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return s.reloadCaddy(ctx)
}

func (s *Service) reloadCaddy(ctx context.Context) error {
	_, err := s.r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "systemctl", Args: []string{"reload", "caddy.service"}})
	return err
}

// writeFile replaces path atomically with exactly mode; the daemon's umask
// would otherwise hide the file from Caddy and from the stack.
func writeFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
