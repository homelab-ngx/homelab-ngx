package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/services"
	"homelab-ngx.com/core/internal/system"
)

// getJSONCmd builds a read-only action that prints the answer to GET path as
// JSON, decoded into T.
func getJSONCmd[T any](name, usage, path string) *cli.Command {
	return &cli.Command{Name: name, Usage: usage, Action: func(c *cli.Context) error {
		v, err := call[T](c.Context, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		return printJSON(v)
	}}
}

func hostCmd() *cli.Command {
	return &cli.Command{Name: "host", Usage: "Inspect host and API state", Subcommands: []*cli.Command{
		getJSONCmd[[]services.ServiceInfo]("core-services", "Show critical infrastructure services", "/core-services"),
		getJSONCmd[system.Version]("version", "Show daemon API version", "/version"),
	}}
}

func configCmd() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "Inspect daemon configuration",
		Subcommands: []*cli.Command{
			getJSONCmd[config.ConfigView]("show", "Dump the current resolved configuration as JSON", "/config"),
		},
	}
}

func daemonCmd() *cli.Command {
	return &cli.Command{
		Name:  "daemon",
		Usage: "Inspect daemon process",
		Subcommands: []*cli.Command{
			{
				Name:  "status",
				Usage: "Lightweight ping to the daemon socket",
				Action: func(c *cli.Context) error {
					_, err := call[system.Version](c.Context, http.MethodGet, "/version", nil)
					if err != nil {
						if isUnreachable(err) {
							if jsonFlag(c) {
								fmt.Println(`{"ok":false,"error":"daemon unreachable"}`)
							} else {
								fmt.Fprintln(os.Stderr, "✗ Daemon unreachable")
							}
							os.Exit(1)
						}
						return err
					}
					if jsonFlag(c) {
						fmt.Println(`{"ok":true}`)
					} else {
						fmt.Println("✔ Daemon is running")
					}
					return nil
				},
			},
		},
	}
}
