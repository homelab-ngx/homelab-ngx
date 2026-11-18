package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/notifications"
)

func notifyCmd() *cli.Command {
	problemFlags := []cli.Flag{
		&cli.StringFlag{Name: "key", Usage: "stable problem key, e.g. backup:appdata", Required: true},
		&cli.StringFlag{Name: "title", Usage: "short headline", Required: true},
		&cli.StringFlag{Name: "body", Usage: "detail text (use --body-stdin for long output)"},
		&cli.BoolFlag{Name: "body-stdin", Usage: "read the detail text from stdin"},
		&cli.StringFlag{Name: "path", Usage: "dashboard path to link to, e.g. /updates", Value: "/"},
	}
	return &cli.Command{
		Name:  "notify",
		Usage: "Send and manage notifications (to every enabled channel)",
		Subcommands: []*cli.Command{
			{
				Name:  "test",
				Usage: "Send a test notification and report which channel carried it",
				Action: func(c *cli.Context) error {
					return runNotify(c.Context, notifications.NotifyRequest{Action: "send", Severity: notifications.Info,
						Title: "Test notification", Body: "Sent from the homelab CLI.", Path: "/"})
				},
			},
			{
				Name:  "push",
				Usage: "Open a problem (alert, deduplicated), or send a one-shot note with --once/--severity info",
				Flags: append([]cli.Flag{
					&cli.StringFlag{Name: "severity", Usage: "alert or info", Value: "alert"},
					&cli.BoolFlag{Name: "once", Usage: "one-shot: no dedup state, send at the given severity"},
				}, problemFlags...),
				Action: func(c *cli.Context) error {
					body := c.String("body")
					if c.Bool("body-stdin") {
						raw, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
						if err != nil {
							return fmt.Errorf("reading body from stdin: %w", err)
						}
						body = strings.TrimSpace(string(raw))
					}
					sev := notifications.Severity(c.String("severity"))
					if sev != notifications.Alert && sev != notifications.Info {
						return fmt.Errorf("--severity must be alert or info")
					}
					req := notifications.NotifyRequest{Key: c.String("key"), Title: c.String("title"), Body: body, Path: c.String("path"), Severity: sev}
					if c.Bool("once") || sev == notifications.Info {
						req.Action = notifications.Send
					} else {
						req.Action = notifications.Report
					}
					return runNotify(c.Context, req)
				},
			},
			{
				Name:  "resolve",
				Usage: "Close a problem; pushes one 'resolved' note if it was open",
				Flags: []cli.Flag{&cli.StringFlag{Name: "key", Required: true}},
				Action: func(c *cli.Context) error {
					return runNotify(c.Context, notifications.NotifyRequest{Action: "resolve", Key: c.String("key")})
				},
			},
		},
	}
}

// runNotify sends the request through the daemon (which owns the dedup state
// and credentials, and is reachable by non-root job users) and falls back to a
// direct local send when the daemon is down — alerting must not depend on it.
func runNotify(ctx context.Context, req notifications.NotifyRequest) error {
	resp, err := postDaemonNotify(ctx, req)
	if err != nil {
		if !isUnreachable(err) {
			return err
		}
		n, nerr := newNotifier()
		if nerr != nil {
			return nerr
		}
		resp, err = n.Apply(ctx, req)
		if err != nil {
			return err
		}
	}
	switch req.Action {
	case notifications.Send:
		fmt.Printf("sent via %s\n", resp.Channel)
	case notifications.Report:
		fmt.Printf("opened=%d reminded=%d\n", len(resp.Opened), len(resp.Reminded))
	default:
		fmt.Printf("resolved=%d\n", len(resp.Resolved))
	}
	return nil
}

func postDaemonNotify(ctx context.Context, req notifications.NotifyRequest) (notifications.NotifyResponse, error) {
	resp, err := call[notifications.NotifyResponse](ctx, http.MethodPost, "/notifications", req)
	if err != nil {
		return notifications.NotifyResponse{}, err
	}
	return *resp, nil
}
