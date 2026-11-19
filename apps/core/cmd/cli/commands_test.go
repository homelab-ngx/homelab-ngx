package main

import (
	"testing"

	"github.com/urfave/cli/v2"
)

func commandNames(commands []*cli.Command) map[string]bool {
	got := make(map[string]bool)
	for _, command := range commands {
		got[command.Name] = true
	}
	return got
}

func TestOperationalCommandsAreExposed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		group *cli.Command
		want  []string
	}{
		{servicesCmd(), []string{"status", "start", "stop", "restart", "logs"}},
		{backupCmd(), []string{"status", "show", "run", "enable", "disable", "logs", "runs", "run-show", "coverage", "dest"}},
		{destCmd(), []string{"list", "add", "set", "rm", "test", "confirm", "reveal"}},
		{storageCmd(), []string{"mount", "unmount", "usage", "subvolume-create", "subvolume-delete"}},
		{hostCmd(), []string{"core-services", "version"}},
		{systemCmd(), []string{"reboot"}},
	}
	for _, tt := range tests {
		got := commandNames(tt.group.Subcommands)
		for _, name := range tt.want {
			if !got[name] {
				t.Errorf("%s missing %s", tt.group.Name, name)
			}
		}
	}
}
