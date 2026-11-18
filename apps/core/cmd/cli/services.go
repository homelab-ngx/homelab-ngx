package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"text/tabwriter"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/services"
)

func servicesCmd() *cli.Command {
	unitAction := func(verb, past string) func(c *cli.Context) error {
		return func(c *cli.Context) error {
			if c.NArg() < 1 {
				return fmt.Errorf("usage: homelab services %s <unit>", verb)
			}
			unit := c.Args().First()
			if err := send(c.Context, http.MethodPost, "/services/"+url.PathEscape(unit)+"/"+verb, nil); err != nil {
				return err
			}
			return emit(c, nil, past+" "+unit)
		}
	}
	return &cli.Command{
		Name:    "services",
		Aliases: []string{"s"},
		Usage:   "Manage homelab services",
		Subcommands: []*cli.Command{
			{
				Name:  "status",
				Usage: "List the service units inside stacks",
				Action: func(c *cli.Context) error {
					list, err := call[[]services.ServiceInfo](c.Context, http.MethodGet, "/services", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(list)
					}
					w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
					fmt.Fprintln(w, "UNIT\tSTACK\tACTIVE")
					for _, s := range *list {
						fmt.Fprintf(w, "%s\t%s\t%v\n", s.UnitName, s.Stack, s.ActiveState == "active")
					}
					return w.Flush()
				},
			},
			{Name: "start", Usage: "Start a service", ArgsUsage: "<unit>", Action: unitAction("start", "Started")},
			{Name: "stop", Usage: "Stop a service", ArgsUsage: "<unit>", Action: unitAction("stop", "Stopped")},
			{Name: "restart", Usage: "Restart a service", ArgsUsage: "<unit>", Action: unitAction("restart", "Restarted")},
			{
				Name:      "logs",
				Usage:     "Tail logs for a service",
				ArgsUsage: "<unit>",
				Action: func(c *cli.Context) error {
					if c.NArg() < 1 {
						return fmt.Errorf("usage: homelab services logs <unit>")
					}
					handleLogs(c.Args().First())
					return nil
				},
			},
		},
	}
}

// handleLogs follows a unit's journal on the user's terminal. It is an
// interactive TTY exec and deliberately bypasses cmdrunner.
func handleLogs(unit string) {
	cmd := exec.Command("journalctl", "-fu", unit)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		die("running journalctl: %v", err)
	}
}
