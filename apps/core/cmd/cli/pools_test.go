package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func poolJSON(operation string) string {
	return fmt.Sprintf(`{"uuid":"u-1","name":"tank","state":"mounted","mountdir":"/var/mnt/tank","data_replicas":2,
		"disks":[{"path":"/dev/sdb","kind":"hdd"},{"path":"/dev/sdc","kind":"hdd"},{"path":"/dev/sdd","kind":"hdd"}],"operation":%s}`, operation)
}

func statusJSON(operation string) string {
	return `{"pools":[` + poolJSON(operation) + `],"subvolumes":[],"partitions":[],
		"unassigned":[{"path":"/dev/sde","kind":"hdd","size":4000,"contents":"ext4 \"Data\"","friendly_name":"Old disk"}]}`
}

// servePool answers the storage status and records the pool requests. The
// status shows no removal, then one that moves, then one that ended with
// errText.
func servePool(t *testing.T, errText string) *[]string {
	t.Helper()
	poolOpPoll = time.Millisecond
	var reads atomic.Int32
	var seen []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/storage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch reads.Add(1) {
		case 1:
			fmt.Fprint(w, statusJSON("null"))
		case 2:
			fmt.Fprint(w, statusJSON(`{"kind":"remove","disk":"/dev/sdc","phase":"moving","started_at":"2026-11-20T10:00:00Z","total_bytes":4000,"left_bytes":1000}`))
		default:
			fmt.Fprint(w, statusJSON(fmt.Sprintf(`{"kind":"remove","disk":"/dev/sdc","phase":"done","started_at":"2026-11-20T10:00:00Z","finished_at":"2026-11-20T11:00:00Z","error":%q,"total_bytes":4000,"left_bytes":0}`, errText)))
		}
	})
	mux.HandleFunc("/api/storage/pools", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, poolJSON("null"))
	})
	mux.HandleFunc("/api/storage/pools/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/disks") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, poolJSON(`{"kind":"remove","disk":"/dev/sdc","phase":"moving","started_at":"2026-11-20T10:00:00Z","total_bytes":4000,"left_bytes":4000}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	serve(t, mux)
	return &seen
}

func TestPoolCommandsSendWhatTheDaemonTakes(t *testing.T) {
	seen := servePool(t, "")
	for _, args := range [][]string{
		{"storage", "pool-create", "--copies", "2", "tank", "/dev/sdb", "/dev/sdc"},
		{"storage", "pool-create", "--erase", "tank", "/dev/sde"},
		{"storage", "pool-add-disk", "tank", "/dev/sde"},
		{"storage", "pool-add-disk", "--erase", "/var/mnt/tank", "/dev/sde"},
		{"storage", "pool-cancel", "tank"},
		{"storage", "pool-delete", "--confirm", "tank", "tank"},
	} {
		if err := runApp(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	var got []string
	for _, s := range *seen {
		method, rest, _ := strings.Cut(s, " ")
		uri, body, _ := strings.Cut(rest, " ")
		if body != "" {
			var compact map[string]any
			if err := json.Unmarshal([]byte(body), &compact); err != nil {
				t.Fatalf("%s: body %q: %v", s, body, err)
			}
			b, _ := json.Marshal(compact)
			body = string(b)
		}
		got = append(got, strings.TrimSpace(method+" "+uri+" "+body))
	}
	want := []string{
		`POST /api/storage/pools {"copies":2,"disks":["/dev/sdb","/dev/sdc"],"name":"tank"}`,
		`POST /api/storage/pools {"confirm":"tank","copies":1,"disks":["/dev/sde"],"name":"tank"}`,
		`POST /api/storage/pools/u-1/disks {"disk":"/dev/sde"}`,
		`POST /api/storage/pools/u-1/disks {"confirm":"tank","disk":"/dev/sde"}`,
		`DELETE /api/storage/pools/u-1/operation`,
		`DELETE /api/storage/pools/u-1?confirm=tank`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPoolRemoveDiskFollowsTheRemovalToItsEnd(t *testing.T) {
	seen := servePool(t, "")
	if err := runApp(t, "storage", "pool-remove-disk", "tank", "/dev/sdc"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 || !strings.HasPrefix((*seen)[0], "DELETE /api/storage/pools/u-1/disks?disk=%2Fdev%2Fsdc") {
		t.Fatalf("requests = %q", *seen)
	}
}

func TestPoolRemoveDiskFailsWhenTheRemovalDid(t *testing.T) {
	servePool(t, "nothing moved for 15m0s: the other disks are full")
	err := runApp(t, "storage", "pool-remove-disk", "tank", "/dev/sdc")
	if err == nil || !strings.Contains(err.Error(), "the other disks are full") {
		t.Fatalf("err = %v", err)
	}
}

func TestStorageStatusListsSpareDisks(t *testing.T) {
	servePool(t, "")
	out, err := captureStdout(t, func() error { return runApp(t, "storage", "status") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tank: 2 copies", "/dev/sdc", "Spare disks", `ext4 "Data"`} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}
