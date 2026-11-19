package main

import (
	"errors"
	"net/http"

	"github.com/urfave/cli/v2"
)

// systemCmd reboots the host.
func systemCmd() *cli.Command {
	return &cli.Command{
		Name:  "system",
		Usage: "Reboot the host",
		Subcommands: []*cli.Command{
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
