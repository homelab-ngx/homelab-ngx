package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/urfave/cli/v2"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/doctor"
	"homelab-ngx.com/core/internal/hostcfg"
	"homelab-ngx.com/core/internal/logging"
)

// doctorChecks builds the check set over the daemon socket and the
// read-only nix config; both work with the daemon down.
func doctorChecks() ([]doctor.Check, error) {
	var (
		once sync.Once
		nix  *config.Nix
		nerr error
	)
	getNix := func() (*config.Nix, error) {
		once.Do(func() { nix, nerr = loadNix() })
		return nix, nerr
	}
	return doctor.Checks(doctor.Deps{
		Daemon: httpClient(),
		Runner: cmdrunner.OS{},
		Nix:    getNix,
		HostTunnel: func(ctx context.Context) (config.CloudflareTunnel, bool) {
			n, err := getNix()
			if err != nil || n == nil {
				return config.CloudflareTunnel{}, false
			}
			st, err := openStore(ctx, n)
			if err != nil {
				return config.CloudflareTunnel{}, false
			}
			defer st.Close()
			return hostcfg.TunnelFromStore(ctx, st)
		},
	}), nil
}

// newAuditor wires streak persistence and the notifier. When the runtime DB
// cannot be opened, streaks live in memory (so nothing can reach the two-run
// alert threshold) and the notifier dedups in memory; a warning says so.
func newAuditor(ctx context.Context, checks []doctor.Check) (doctor.Auditor, func()) {
	log := logging.Logger("doctor")
	a := doctor.Auditor{Streaks: doctor.NewMemStreaks(), Known: doctor.Names(checks), Log: log}
	nix, err := loadNix()
	if err != nil {
		log.Warn("nix config unavailable; alerts cannot be delivered", "error", err.Error())
		return a, func() {}
	}
	st, err := openStore(ctx, nix)
	if err != nil {
		log.Warn("runtime DB unavailable; streaks are in memory so no alert will open", "error", err.Error())
		a.Sink = notifierWith(nix, nil)
		return a, func() {}
	}
	a.Streaks = doctor.NewDBStreaks(st)
	a.Sink = notifierWith(nix, st)
	return a, func() { _ = st.Close() }
}

func doctorCmd() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "Run diagnostic health checks",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "json",
				Usage: "output report as JSON (always exits 0 unless --fail-on-error)",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "list every passing check with its detail",
			},
			&cli.BoolFlag{
				Name:  "fail-on-error",
				Usage: "exit 1 if any check fails",
			},
			&cli.StringFlag{
				Name:  "check",
				Usage: "comma-separated list of check slugs to run (default: all)",
			},
		},
		Subcommands: []*cli.Command{
			{
				Name:  "daily",
				Usage: "Run the daily host audit, notify on failures, and ping Healthchecks.io",
				Action: func(c *cli.Context) error {
					return handleDoctorDaily(c.Context)
				},
			},
			{
				Name:  "notify",
				Usage: "Read a JSON doctor report from stdin and alert on failures like the daily audit",
				Action: func(c *cli.Context) error {
					return handleDoctorNotify(c.Context)
				},
			},
		},
		Action: func(c *cli.Context) error {
			return handleDoctor(c)
		},
	}
}

func handleDoctor(c *cli.Context) error {
	asJSON := c.Bool("json") || jsonFlag(c)

	var slugs []string
	for _, s := range strings.Split(c.String("check"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			slugs = append(slugs, s)
		}
	}
	all, err := doctorChecks()
	if err != nil {
		return err
	}
	checks, err := doctor.Select(all, slugs)
	if err != nil {
		return err
	}
	report := doctor.Runner{}.Run(c.Context, checks)

	switch {
	case asJSON:
		if err := printJSON(report); err != nil {
			return err
		}
	case c.Bool("verbose"):
		fmt.Print(doctor.FormatReportVerbose(report))
	default:
		fmt.Print(doctor.FormatReportText(report))
	}
	if c.Bool("fail-on-error") && report.Failed > 0 {
		os.Exit(1)
	}
	return nil
}

// handleDoctorDaily is the daily audit: run, advance streaks, reconcile the
// audit:<check> alerts, ping the healthcheck, print one summary line. It exits
// 0 whenever the audit ran; failing checks and undeliverable alerts are
// reported, not errors.
func handleDoctorDaily(ctx context.Context) error {
	checks, err := doctorChecks()
	if err != nil {
		return err
	}
	auditor, closeFn := newAuditor(ctx, checks)
	defer closeFn()
	res := doctor.RunDaily(ctx, doctor.DailyConfig{
		Checks:  checks,
		Auditor: auditor,
		Ping:    doctor.NewHealthcheckPinger(nil, os.Getenv("HOMELAB_HEALTHCHECK_UUID")),
	})
	fmt.Println(doctor.DailySummary(res))
	return nil
}

// handleDoctorNotify applies a JSON report from stdin (the output of
// `homelab doctor --json`) to the same streak and alert rules as the daily
// audit.
func handleDoctorNotify(ctx context.Context) error {
	var report doctor.Report
	if err := json.NewDecoder(os.Stdin).Decode(&report); err != nil {
		return fmt.Errorf("reading report from stdin: %w", err)
	}
	checks, err := doctorChecks()
	if err != nil {
		return err
	}
	auditor, closeFn := newAuditor(ctx, checks)
	defer closeFn()
	out := auditor.Apply(ctx, report)
	fmt.Println(doctor.DailySummary(doctor.DailyResult{Report: report, Outcome: out}))
	return nil
}
