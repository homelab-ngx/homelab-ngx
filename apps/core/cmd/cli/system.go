package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/sysupdates"
)

// systemCmd mirrors the Updates page's System card: status and reboot.
func systemCmd() *cli.Command {
	return &cli.Command{
		Name:  "system",
		Usage: "NixOS release, newer release, reboot (the Updates page's System card)",
		Subcommands: []*cli.Command{
			{
				Name:  "status",
				Usage: "Release, newer release, reboot needed",
				Action: func(c *cli.Context) error {
					st, err := systemStatus(c.Context)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(st)
					}
					printSystemStatus(st)
					return nil
				},
			},
			{
				Name:  "reboot",
				Usage: "Reboot the host",
				Flags: []cli.Flag{&cli.BoolFlag{Name: "yes", Usage: "required: confirm the reboot"}},
				Action: func(c *cli.Context) error {
					if !c.Bool("yes") {
						return errors.New("rebooting takes every service down for a few minutes; pass --yes to confirm")
					}
					if err := send(c.Context, http.MethodPost, "/system-updates/reboot", nil); err != nil {
						return err
					}
					return emit(c, map[string]any{"success": true}, "✔ Rebooting in a few seconds.")
				},
			},
		},
	}
}

func systemStatus(ctx context.Context) (*sysupdates.SystemUpdateStatus, error) {
	return call[sysupdates.SystemUpdateStatus](ctx, http.MethodGet, "/system-updates/status", nil)
}

func printSystemStatus(st *sysupdates.SystemUpdateStatus) {
	rel := st.Release.Current
	if rel == "" {
		rel = "unknown"
	}
	if st.Release.Pinned {
		rel += " (pre-release snapshot)"
	}
	fmt.Printf("NixOS %s\n", rel)
	if st.RebootRequired {
		fmt.Println("⚠ Reboot needed: the running kernel is not the installed one (homelab system reboot --yes)")
	}
	if st.Upgrade != nil {
		pre := ""
		if st.Upgrade.PreRelease {
			pre = " (pre-release)"
		}
		fmt.Printf("Newer release: NixOS %s%s\n", st.Upgrade.Release, pre)
	}
}
