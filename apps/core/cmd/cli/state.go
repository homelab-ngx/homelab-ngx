package main

import (
	"context"
	"fmt"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store"
)

// nixConfigPath is the read-only config the daemon module renders. The CLI
// reads it directly so notify and doctor work with the daemon down.
const nixConfigPath = "/etc/homelab-daemon/config.json"

// loadNix reads nixConfigPath.
func loadNix() (*config.Nix, error) { return config.LoadNix(nixConfigPath) }

// openStore opens the runtime DB without migrating it. The CLI touches the
// DB only for the notify fallback and the doctor streaks.
func openStore(ctx context.Context, nix *config.Nix) (*store.Store, error) {
	return store.Open(ctx, nix.Paths.DB, store.Options{Migrate: false})
}

// newNotifier builds the notifier used when the daemon is unreachable: the
// channels come from the DB the nix config names, the dashboard link from
// HOMELAB_LINK_BASE. If the DB cannot be opened (a non-root caller, a
// schema the daemon has not migrated yet) there is no channel to send to.
func newNotifier() (*notifications.Notifier, error) {
	nix, err := loadNix()
	if err != nil {
		return nil, fmt.Errorf("loading notification config: %w", err)
	}
	return notifierFor(context.Background(), nix), nil
}

func notifierFor(ctx context.Context, nix *config.Nix) *notifications.Notifier {
	s, err := openStore(ctx, nix)
	if err != nil {
		logging.Logger("notifications").Warn("notification state unavailable; deduplicating in memory only", "error", err.Error())
		s = nil
	}
	// The process exits right after one command; the handle is released then.
	return notifierWith(nix, s)
}

// ageKeyFile is the host age key the channels' tokens are sealed with; a
// variable for tests.
var ageKeyFile = stacks.DefaultHost(cmdrunner.OS{}).AgeKeyFile

// notifierWith builds the notifier over an already opened store (nil =
// in-memory state and no channels).
func notifierWith(_ *config.Nix, s *store.Store) *notifications.Notifier {
	var st notifications.Store // nil = in-memory
	cfg := notifications.Config{LinkBase: notifications.LinkBase()}
	if s != nil {
		st = notifications.NewDBStore(s)
		cfg.Targets = notifications.NewChannels(s, ageKeyFile)
	}
	return notifications.New(cfg, st, nil)
}
