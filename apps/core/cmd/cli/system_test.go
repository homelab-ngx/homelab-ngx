package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/metrics"
	"homelab-ngx.com/core/internal/power"
	"homelab-ngx.com/core/internal/powermeter"
	"homelab-ngx.com/core/internal/powerprofile"
)

func TestRebootAsksForConfirmation(t *testing.T) {
	err := runApp(t, "system", "reboot")
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
}

func TestRebootFollowsTheStepsUntilTheHostGoesDown(t *testing.T) {
	powerPoll = time.Millisecond
	var asked power.PowerRequest
	var reads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/system/power", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"phase":"idle","steps":[],"busy":[],"boot_id":"b1","expected_seconds":90}`)
	})
	mux.HandleFunc("GET /api/system/power", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if reads.Add(1) == 1 {
			fmt.Fprint(w, `{"action":"reboot","phase":"stopping_stacks","steps":[{"kind":"stack","name":"immich","state":"working"}],"busy":[],"boot_id":"b1","expected_seconds":90}`)
			return
		}
		fmt.Fprint(w, `{"action":"reboot","phase":"going_down","steps":[{"kind":"stack","name":"immich","state":"done"},{"kind":"mount","name":"/var/mnt/media","state":"done"}],"busy":[],"boot_id":"b1","expected_seconds":90}`)
	})
	serve(t, mux)
	if err := runApp(t, "system", "reboot", "--yes", "--force"); err != nil {
		t.Fatal(err)
	}
	if asked.Action != power.Reboot || !asked.Force {
		t.Fatalf("asked %+v", asked)
	}
	if reads.Load() != 2 {
		t.Fatalf("read the status %d times, want 2", reads.Load())
	}
}

func TestShutdownIsPoweroff(t *testing.T) {
	var asked power.PowerRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/system/power", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&asked)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"phase":"idle","steps":[],"busy":[],"boot_id":"b1","expected_seconds":90}`)
	})
	serve(t, mux)
	if err := runApp(t, "system", "shutdown", "--yes", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	if asked.Action != power.PowerOff || asked.Force {
		t.Fatalf("asked %+v", asked)
	}
}

func TestRebootWithFrozenStacksAsksForThawOrForce(t *testing.T) {
	stdinIsTerminal = func() bool { return false }
	var asked []power.PowerRequest
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/system/power", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"phase":"idle","steps":[],"busy":[],"frozen":["immich"],"boot_id":"b1","expected_seconds":90}`)
	})
	mux.HandleFunc("POST /api/system/power", func(w http.ResponseWriter, r *http.Request) {
		var req power.PowerRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		asked = append(asked, req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"phase":"idle","steps":[],"busy":[],"frozen":[],"boot_id":"b1","expected_seconds":90}`)
	})
	serve(t, mux)
	err := runApp(t, "system", "reboot", "--yes", "--no-wait")
	if err == nil || !strings.Contains(err.Error(), "immich") || !strings.Contains(err.Error(), "--thaw") || len(asked) != 0 {
		t.Fatalf("err = %v, asked %+v", err, asked)
	}
	if err := runApp(t, "system", "reboot", "--yes", "--no-wait", "--thaw"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !asked[0].Thaw || asked[0].Force {
		t.Fatalf("asked %+v", asked)
	}
}

func TestStepText(t *testing.T) {
	if got := phaseText(power.PhaseBackups); got != "stopping the backups" {
		t.Errorf("phaseText(PhaseBackups) = %q, want %q", got, "stopping the backups")
	}
	for _, tt := range []struct {
		s    power.PowerStep
		want string
	}{
		{power.PowerStep{Kind: power.StepBackup, Name: "Nightly", State: power.StateWorking}, "… stopping backup Nightly"},
		{power.PowerStep{Kind: power.StepBackup, Name: "Nightly", State: power.StateDone}, "✔ backup Nightly stopped"},
		{power.PowerStep{Kind: power.StepBackup, Name: "Nightly", State: power.StateFailed, Detail: "timed out"}, "✘ backup Nightly: timed out"},
		{power.PowerStep{Kind: power.StepStack, Name: "immich", State: power.StateDone}, "✔ stack immich stopped"},
		{power.PowerStep{Kind: power.StepMount, Name: "/var/mnt/media", State: power.StateWorking}, "… unmounting /var/mnt/media"},
		{power.PowerStep{Kind: power.StepMount, Name: "/var/mnt/media", State: power.StateFailed, Detail: "still in use"}, "✘ /var/mnt/media: still in use"},
	} {
		if got := stepText(tt.s); got != tt.want {
			t.Errorf("stepText(%+v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestProfileSetSendsTheProfileAndGovernor(t *testing.T) {
	var asked []powerprofile.PowerProfileSettings
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/system/power-profile", func(w http.ResponseWriter, r *http.Request) {
		var in powerprofile.PowerProfileSettings
		_ = json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"profile":%q,"governor":%q,"read_only":false,"driver":"intel_pstate","governors":["performance","powersave"],"tunables":[],"idle":{"window_seconds":60,"active":0.1,"cpu_states":[],"package_states":[],"deepest":"PC6","watts":1.2},"blockers":[]}`, in.Profile, in.Governor)
	})
	serve(t, mux)
	if err := runApp(t, "system", "profile", "set", "--governor", "performance", "power-saver"); err != nil {
		t.Fatal(err)
	}
	if err := runApp(t, "system", "profile", "set", "--governor", "default", "balanced"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0].Profile != powerprofile.PowerSaver || asked[0].Governor != "performance" ||
		asked[1].Profile != powerprofile.Balanced || asked[1].Governor != "" {
		t.Fatalf("asked %+v", asked)
	}
	if err := runApp(t, "system", "profile", "set"); err == nil {
		t.Fatal("set without a profile went ahead")
	}
}

func TestPowerLines(t *testing.T) {
	w := 14.24
	m := &powermeter.PowerMeter{Entity: "sensor.plug_power", State: powermeter.StateConnected, Watts: &w}
	p := &metrics.MetricsPowerProfiles{Range: metrics.Range7d, Profiles: []metrics.MetricsProfilePower{
		{Profile: "balanced", Minutes: 90, Wall: 16.04, CPU: 2.8},
		{Profile: "power-saver", Minutes: 30, Wall: metrics.Num(math.NaN()), CPU: 0.9},
		{Profile: "performance", Minutes: 600, Wall: 31, CPU: 12.25},
	}}
	got := strings.Join(powerLines(m, p), "\n")
	want := strings.Join([]string{
		"meter:    14.2 W at the wall (sensor.plug_power)",
		"7 days:",
		"  balanced     wall 16.0 W   cpu 2.8 W    over 1.5 h",
		"  power-saver  wall -        cpu 0.9 W    over 30 min",
		"  performance  wall 31.0 W   cpu 12.2 W   over 10.0 h",
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	failed := &powermeter.PowerMeter{Entity: "sensor.plug_power", State: powermeter.StateAuthFailed, Detail: "Home Assistant refused the token"}
	if got := powerLines(failed, nil); len(got) != 1 || got[0] != "meter:    sensor.plug_power, auth_failed: Home Assistant refused the token" {
		t.Errorf("failed: %q", got)
	}
	if got := powerLines(&powermeter.PowerMeter{State: powermeter.StateOff}, &metrics.MetricsPowerProfiles{}); len(got) != 0 {
		t.Errorf("off: %q", got)
	}
}
