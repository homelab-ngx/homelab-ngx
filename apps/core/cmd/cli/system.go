package main

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/metrics"
	"homelab-ngx.com/core/internal/power"
	"homelab-ngx.com/core/internal/powermeter"
	"homelab-ngx.com/core/internal/powerprofile"
)

// systemCmd reboots and powers off the host, and sets its power profile.
func systemCmd() *cli.Command {
	return &cli.Command{
		Name:  "system",
		Usage: "Reboot or power off the host, or set its power profile",
		Subcommands: []*cli.Command{
			powerCmd(power.Reboot, "Stop the stacks, unmount the pools, then reboot", nil),
			powerCmd(power.PowerOff, "Stop the stacks, unmount the pools, then power off", []string{"shutdown"}),
			{
				Name:  "status",
				Usage: "Show a shutdown under way or scheduled, and what one would interrupt",
				Action: func(c *cli.Context) error {
					st, err := call[power.PowerStatus](c.Context, http.MethodGet, "/system/power", nil)
					if err != nil {
						return err
					}
					if jsonFlag(c) {
						return printJSON(st)
					}
					printPowerStatus(*st)
					return nil
				},
			},
			profileCmd(),
			{
				Name:  "cancel",
				Usage: "Call off the scheduled reboot or poweroff",
				Action: func(c *cli.Context) error {
					if err := send(c.Context, http.MethodDelete, "/system/power/scheduled", nil); err != nil {
						return err
					}
					return emit(c, map[string]any{"success": true}, "✔ The scheduled shutdown is called off.")
				},
			},
		},
	}
}

func printPowerStatus(st power.PowerStatus) {
	if st.Action != "" {
		fmt.Printf("%s: %s\n", st.Action, phaseText(st.Phase))
		for _, s := range st.Steps {
			fmt.Println(" ", stepText(s))
		}
	}
	if sch := st.Scheduled; sch != nil {
		fmt.Printf("%s scheduled at %s\n", sch.Action, sch.At.Local().Format("2006-01-02 15:04"))
	}
	for _, b := range st.Busy {
		fmt.Println("running:", b)
	}
	if st.Action == "" && st.Scheduled == nil && len(st.Busy) == 0 {
		fmt.Println("no shutdown under way or scheduled")
	}
}

// powerPoll is how often a shutdown is followed; a var for tests.
var powerPoll = time.Second

func powerCmd(a power.PowerAction, usage string, aliases []string) *cli.Command {
	return &cli.Command{
		Name:    string(a),
		Aliases: aliases,
		Usage:   usage,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "yes", Usage: "required: confirm it"},
			&cli.BoolFlag{Name: "force", Usage: "go ahead even when it interrupts stack operations, an update or backups; frozen stacks are killed and lose what they held"},
			&cli.BoolFlag{Name: "thaw", Usage: "thaw the frozen stacks, so they stop cleanly"},
			&cli.BoolFlag{Name: "no-wait", Usage: "return once it has started, without following it"},
		},
		Action: func(c *cli.Context) error {
			if !c.Bool("yes") {
				return fmt.Errorf("a %s takes every service down; pass --yes to confirm", a)
			}
			req := power.PowerRequest{Action: a, Force: c.Bool("force"), Thaw: c.Bool("thaw")}
			if !req.Force && !req.Thaw {
				if err := askFrozen(c, &req); err != nil {
					return err
				}
			}
			if _, err := call[power.PowerStatus](c.Context, http.MethodPost, "/system/power", req); err != nil {
				return err
			}
			if c.Bool("no-wait") {
				return emit(c, map[string]any{"success": true}, "✔ The "+string(a)+" has started.")
			}
			return followPower(c, a)
		},
	}
}

// stdinIsTerminal reports whether there is someone to ask; a seam for tests.
var stdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// askFrozen asks what to do with the frozen stacks a shutdown would lose:
// thaw them, cancel, or go on and kill them. Without a terminal to ask on it
// names them and the flags.
func askFrozen(c *cli.Context, req *power.PowerRequest) error {
	st, err := call[power.PowerStatus](c.Context, http.MethodGet, "/system/power", nil)
	if err != nil || len(st.Frozen) == 0 {
		return nil // the daemon refuses with stacks_frozen if one froze since
	}
	names := strings.Join(st.Frozen, ", ")
	refuse := fmt.Errorf("frozen stacks lose what they hold at a %s: %s. Pass --thaw to stop them cleanly, or --force to kill them", req.Action, names)
	if !stdinIsTerminal() {
		return refuse
	}
	fmt.Fprintf(os.Stderr, "Frozen stacks lose what they hold at a %s: %s.\n", req.Action, names)
	for {
		ans, err := readLine("[t]haw them and go on, [c]ancel, or [k]ill them and go on? ")
		if err != nil {
			return refuse
		}
		switch strings.ToLower(ans) {
		case "t", "thaw":
			req.Thaw = true
			return nil
		case "k", "kill":
			req.Force = true
			return nil
		case "c", "cancel", "":
			return fmt.Errorf("the %s was cancelled", req.Action)
		}
	}
}

// followPower prints each step as it changes, until the daemon hands the
// host to systemd or stops answering: it goes down with the host.
func followPower(c *cli.Context, a power.PowerAction) error {
	shown := map[string]string{}
	phase := power.PowerPhase("")
	for {
		st, err := call[power.PowerStatus](c.Context, http.MethodGet, "/system/power", nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "✔ The daemon has stopped; the host is going down.")
			return nil
		}
		if st.Phase != phase {
			phase = st.Phase
			if phase != power.PhaseIdle {
				fmt.Fprintln(os.Stderr, "►", phaseText(phase))
			}
		}
		for _, s := range st.Steps {
			key := string(s.Kind) + " " + s.Name
			if line := stepText(s); shown[key] != line && s.State != power.StateWaiting {
				shown[key] = line
				fmt.Fprintln(os.Stderr, " ", line)
			}
		}
		switch st.Phase {
		case power.PhaseFailed:
			return fmt.Errorf("%s", st.Error)
		case power.PhaseDown:
			if a == power.PowerOff {
				fmt.Fprintln(os.Stderr, "✔ Powering off.")
			} else {
				fmt.Fprintln(os.Stderr, "✔ Rebooting.")
			}
			return nil
		}
		select {
		case <-c.Context.Done():
			return c.Context.Err()
		case <-time.After(powerPoll):
		}
	}
}

func phaseText(p power.PowerPhase) string {
	switch p {
	case power.PhaseBackups:
		return "stopping the backups"
	case power.PhaseStacks:
		return "stopping the stacks"
	case power.PhaseMounts:
		return "unmounting the pools"
	case power.PhaseDown:
		return "handed to systemd"
	case power.PhaseFailed:
		return "failed"
	}
	return string(p)
}

func stepText(s power.PowerStep) string {
	what, done, doing := "stack "+s.Name, " stopped", "stopping "
	switch s.Kind {
	case power.StepBackup:
		what = "backup " + s.Name
	case power.StepMount:
		what, done, doing = s.Name, " unmounted", "unmounting "
	}
	switch s.State {
	case power.StateDone:
		return "✔ " + what + done
	case power.StateFailed:
		return "✘ " + what + ": " + s.Detail
	case power.StateWorking:
		return "… " + doing + what
	}
	return "· " + what
}

// profileCmd shows and sets the power profile.
func profileCmd() *cli.Command {
	return &cli.Command{
		Name:  "profile",
		Usage: "Show the power profile, how deep the host idles and what keeps it awake",
		Action: func(c *cli.Context) error {
			st, err := call[powerprofile.PowerProfileStatus](c.Context, http.MethodGet, "/system/power-profile", nil)
			if err != nil {
				return err
			}
			if jsonFlag(c) {
				return printJSON(st)
			}
			printProfile(*st)
			// The meter and the comparison are extras: a daemon without
			// them still shows the profile.
			m, _ := call[powermeter.PowerMeter](c.Context, http.MethodGet, "/system/power-meter", nil)
			q := "/metrics/power-profiles?range=7d"
			if m != nil && m.IdleOnly {
				q += "&idle_only=true"
			}
			p, _ := call[metrics.MetricsPowerProfiles](c.Context, http.MethodGet, q, nil)
			for _, l := range powerLines(m, p) {
				fmt.Println(l)
			}
			return nil
		},
		Subcommands: []*cli.Command{{
			Name:      "set",
			Usage:     "Choose power-saver, balanced or performance (flags go before the profile)",
			ArgsUsage: "<power-saver|balanced|performance>",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "governor", Usage: "a CPU governor of the driver's, or default for the profile's own"},
			},
			Action: func(c *cli.Context) error {
				if c.NArg() != 1 {
					return fmt.Errorf("usage: homelab system profile set [--governor NAME] <power-saver|balanced|performance>")
				}
				in := powerprofile.PowerProfileSettings{Profile: powerprofile.PowerProfile(c.Args().First())}
				if g := c.String("governor"); g != "default" {
					in.Governor = g
				}
				st, err := call[powerprofile.PowerProfileStatus](c.Context, http.MethodPut, "/system/power-profile", in)
				if err != nil {
					return err
				}
				if jsonFlag(c) {
					return printJSON(st)
				}
				printProfile(*st)
				return nil
			},
		}},
	}
}

// powerLines are the power meter's reading and the average power per
// profile over the last 7 days; either may be nil.
func powerLines(m *powermeter.PowerMeter, p *metrics.MetricsPowerProfiles) []string {
	var out []string
	if m != nil && m.State != powermeter.StateOff {
		line := fmt.Sprintf("meter:    %s, %s", m.Entity, m.State)
		if m.Watts != nil {
			line = fmt.Sprintf("meter:    %.1f W at the wall (%s)", *m.Watts, m.Entity)
		} else if m.Detail != "" {
			line += ": " + m.Detail
		}
		out = append(out, line)
	}
	if p == nil || len(p.Profiles) == 0 {
		return out
	}
	head := "7 days:"
	if p.IdleOnly {
		head = "7 days, idle minutes:"
	}
	out = append(out, head)
	w := func(n metrics.Num) string {
		if math.IsNaN(float64(n)) {
			return "-"
		}
		return fmt.Sprintf("%.1f W", float64(n))
	}
	for _, r := range p.Profiles {
		over := fmt.Sprintf("%.1f h", float64(r.Minutes)/60)
		if r.Minutes < 60 {
			over = fmt.Sprintf("%d min", r.Minutes)
		}
		out = append(out, fmt.Sprintf("  %-12s wall %-8s cpu %-8s over %s", r.Profile, w(r.Wall), w(r.CPU), over))
	}
	return out
}

func printProfile(st powerprofile.PowerProfileStatus) {
	gov := st.Governor
	if gov == "" {
		gov = "the profile's"
	}
	fmt.Printf("profile:  %s\n", st.Profile)
	if st.Driver != "" {
		fmt.Printf("governor: %s (%s; %s)\n", gov, st.Driver, strings.Join(st.Governors, ", "))
	}
	if st.ReadOnly {
		fmt.Println("read only: the host is not on the image")
	}
	idle := st.Idle
	line := fmt.Sprintf("idle:     %.0f%% over %.0f s", 100*(1-idle.Active), idle.WindowSeconds)
	if idle.Deepest != "" {
		line += ", deepest package state " + idle.Deepest
	}
	if idle.Watts != nil {
		line += fmt.Sprintf(", %.1f W", *idle.Watts)
	}
	fmt.Println(line)
	for _, s := range idle.CPUStates {
		fmt.Printf("  %-10s %5.1f%%\n", s.Name, 100*s.Share)
	}
	for _, s := range idle.PackageStates {
		fmt.Printf("  %-10s %5.1f%%\n", s.Name, 100*s.Share)
	}
	for _, t := range st.Tunables {
		v := t.Value
		if v == "" {
			v = "boot value"
		}
		line := fmt.Sprintf("  %-16s %-11s %s", t.Name, t.State, v)
		if t.Refused > 0 {
			line += fmt.Sprintf(" (%d of %d refused)", t.Refused, t.Files)
		}
		if t.Detail != "" {
			line += ": " + t.Detail
		}
		fmt.Println(line)
	}
	for _, b := range st.Blockers {
		name := strings.TrimSpace(strings.Join([]string{b.ID, b.Driver, b.Name}, " "))
		fmt.Printf("awake: %s %s (%s); %s turns it off\n", b.Device, name, b.Kind, b.Fix)
	}
}
