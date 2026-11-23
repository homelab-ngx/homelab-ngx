package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/urfave/cli/v2"
)

// stackShellCmd is `homelab stacks shell`: root's login shell in a running
// stack, or one command there. It hands the terminal to machinectl, an
// interactive TTY exec that deliberately bypasses cmdrunner.
func stackShellCmd() *cli.Command {
	return &cli.Command{
		Name:      "shell",
		Usage:     "Open a root shell in a running stack, or run one command there",
		ArgsUsage: "<stack> [-- <command> [args...]]",
		Description: "Without a command, root's login shell. A command that is not a path is looked for in\n" +
			"the stack's system profile (/run/current-system/sw/bin). Press Ctrl+] three times\n" +
			"within a second to leave a shell that does not answer.",
		Action: func(c *cli.Context) error {
			if err := requireArgs(c, 1, "homelab stacks shell <stack> [-- <command> [args...]]"); err != nil {
				return err
			}
			name := c.Args().First()
			s, err := getStack(c, name)
			if err != nil {
				return err
			}
			switch {
			case !s.Running:
				return fmt.Errorf("stack %q is not running; start the stack first", name)
			case s.Frozen:
				return fmt.Errorf("stack %q is frozen; thaw it first", name)
			}
			bin, err := exec.LookPath("machinectl")
			if err != nil {
				return err
			}
			args := append([]string{"machinectl", "shell", "root@" + name}, shellCommand(c.Args().Tail())...)
			return syscall.Exec(bin, args, os.Environ())
		},
	}
}

// shellCommand is the command machinectl runs in the stack: machinectl
// wants an absolute path, so a bare name is taken from the system profile.
// A "--" after the stack's name reaches here as an argument; it only marks
// where the command starts.
func shellCommand(argv []string) []string {
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return nil
	}
	out := append([]string(nil), argv...)
	if !strings.HasPrefix(out[0], "/") {
		out[0] = "/run/current-system/sw/bin/" + out[0]
	}
	return out
}
