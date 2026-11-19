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
