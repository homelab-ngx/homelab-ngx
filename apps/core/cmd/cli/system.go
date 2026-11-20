package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/power"
)

// systemCmd reboots and powers off the host.
func systemCmd() *cli.Command {
	return &cli.Command{
		Name:  "system",
		Usage: "Reboot or power off the host",
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
			&cli.BoolFlag{Name: "force", Usage: "go ahead even when it interrupts stack operations, an update or backups"},
			&cli.BoolFlag{Name: "no-wait", Usage: "return once it has started, without following it"},
		},
		Action: func(c *cli.Context) error {
			if !c.Bool("yes") {
				return fmt.Errorf("a %s takes every service down; pass --yes to confirm", a)
			}
			req := power.PowerRequest{Action: a, Force: c.Bool("force")}
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
	if s.Kind == power.StepMount {
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
