package main

import "testing"

func TestDailyAuditIsDoctorSubcommand(t *testing.T) {
	t.Parallel()
	doctorDaily := false
	for _, command := range doctorCmd().Subcommands {
		if command.Name == "daily" {
			doctorDaily = true
			break
		}
	}
	if !doctorDaily {
		t.Fatal("doctor command does not expose the daily health audit")
	}

	for _, command := range servicesCmd().Subcommands {
		if command.Name == "daily" {
			t.Fatal("daily health audit is incorrectly exposed under services")
		}
	}
}

func TestSecretCommandsExposePipeSafeFlags(t *testing.T) {
	t.Parallel()
	cmd := secretCmd()
	if cmd.Before != nil {
		t.Fatal("secret group must not block help with a privilege check")
	}
	want := map[string]map[string]bool{
		"add": {"value-stdin": false, "description": false},
		"set": {"value-stdin": false},
	}
	for _, sub := range cmd.Subcommands {
		flags, ok := want[sub.Name]
		if !ok {
			continue
		}
		for _, flag := range sub.Flags {
			for _, name := range flag.Names() {
				if _, exists := flags[name]; exists {
					flags[name] = true
				}
			}
		}
	}
	for command, flags := range want {
		for flag, found := range flags {
			if !found {
				t.Errorf("secret %s missing --%s", command, flag)
			}
		}
	}
}
