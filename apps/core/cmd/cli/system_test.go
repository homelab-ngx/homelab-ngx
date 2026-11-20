package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/power"
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

func TestStepText(t *testing.T) {
	for _, tt := range []struct {
		s    power.PowerStep
		want string
	}{
		{power.PowerStep{Kind: power.StepStack, Name: "immich", State: power.StateDone}, "✔ stack immich stopped"},
		{power.PowerStep{Kind: power.StepMount, Name: "/var/mnt/media", State: power.StateWorking}, "… unmounting /var/mnt/media"},
		{power.PowerStep{Kind: power.StepMount, Name: "/var/mnt/media", State: power.StateFailed, Detail: "still in use"}, "✘ /var/mnt/media: still in use"},
	} {
		if got := stepText(tt.s); got != tt.want {
			t.Errorf("stepText(%+v) = %q, want %q", tt.s, got, tt.want)
		}
	}
}
