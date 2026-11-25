package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/stacks"
)

// stackJSON is a stack as the daemon reports it, with the given operation.
func stackJSON(operation string) string {
	rev := `{"rev":"aaaaaaa1","url":"github:o/r/aaaaaaa1","at":"2026-11-15T21:00:00Z","manifest":{"version":1,"name":"immich","routes":[],"storage":[],"gpu":false,"egress":[],"health":{"units":[]}}}`
	return fmt.Sprintf(`{"name":"immich","source":"github:o/r","created_at":"2026-11-15T21:00:00Z","fetched":%s,"approved":%s,"new_grants":[],
		"running":true,"want_running":true,"generation":1,"generations":[],"address":"10.99.0.2","operation":%s,
		"variables_ready":false,"restart_needed":false,
		"folders":[{"path":"/pool/music","mount":"/music","read_only":true,"updated_at":"2026-11-16T12:00:00Z"}],"variables":[
		{"name":"DB_PASSWORD","description":"Database password","declared":true,"kind":"secret","required":true,"source":"stored","secret":true,"set":true,"in_env":true,"updated_at":"2026-11-16T12:00:00Z"},
		{"name":"API_KEY","description":"","declared":true,"kind":"secret","required":true,"source":"unset","secret":true,"set":false,"in_env":false},
		{"name":"MEDIA_DIR","description":"Library","declared":true,"kind":"path","required":true,"default":"/pool/media","mount":"/media","read_only":false,"source":"stored","secret":false,"set":true,"value":"/pool/films","in_env":false,"updated_at":"2026-11-16T12:00:00Z"},
		{"name":"TZ","description":"","declared":true,"kind":"env","required":false,"default":"UTC","source":"default","secret":false,"set":false,"in_env":true},
		{"name":"GREETING","description":"","declared":false,"kind":"env","required":false,"source":"stored","secret":false,"set":true,"value":"two\nlines","in_env":false,"updated_at":"2026-11-16T12:00:00Z"}]}`, rev, rev, operation)
}

// serveStackOp answers the update with a running operation, then two reads:
// still running, then finished with errText.
func serveStackOp(t *testing.T, errText string) *atomic.Int32 {
	t.Helper()
	stackOpPoll = time.Millisecond
	var reads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/stacks/immich/update", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, stackJSON(`{"kind":"update","phase":"building aaaaaaa1","started_at":"2026-11-15T22:00:00Z"}`))
	})
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if reads.Add(1) == 1 {
			fmt.Fprint(w, stackJSON(`{"kind":"update","phase":"waiting for generation 2 to be healthy","started_at":"2026-11-15T22:00:00Z"}`))
			return
		}
		fmt.Fprint(w, stackJSON(fmt.Sprintf(`{"kind":"update","phase":"running generation 2 (aaaaaaa1)","started_at":"2026-11-15T22:00:00Z","finished_at":"2026-11-15T22:01:00Z","error":%q}`, errText)))
	})
	serve(t, mux)
	return &reads
}

func TestStackUpdateFollowsTheOperationToItsEnd(t *testing.T) {
	reads := serveStackOp(t, "")
	if err := runApp(t, "stacks", "update", "immich"); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 {
		t.Errorf("the stack was read %d times, want 2", reads.Load())
	}
}

func TestStackUpdateFailsWhenTheOperationDid(t *testing.T) {
	serveStackOp(t, "generation 2 was not healthy; rolled back to generation 1")
	err := runApp(t, "stacks", "update", "immich")
	if err == nil || !strings.Contains(err.Error(), "rolled back to generation 1") {
		t.Errorf("err = %v", err)
	}
}

// captureStdout returns what fn printed to stdout.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	ferr := fn()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b), ferr
}

func TestStackShowPrintsTheGrantedAPIScopes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/ha", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := strings.ReplaceAll(stackJSON("null"), `"name":"immich","routes"`, `"name":"ha","api":["read","control"],"routes"`)
		fmt.Fprint(w, strings.Replace(s, `"name":"immich"`, `"name":"ha"`, 1))
	})
	serve(t, mux)
	out, err := captureStdout(t, func() error { return runApp(t, "stacks", "show", "ha") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "API:      read, control (on 10.99.0.1 port 7781)\n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestStackShowSaysWhenARestartIsNeeded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, strings.Replace(stackJSON("null"), `"restart_needed":false`, `"restart_needed":true`, 1))
	})
	serve(t, mux)
	out, err := captureStdout(t, func() error { return runApp(t, "stacks", "show", "immich") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Restart needed: the container runs with other paths than its values give now. Apply them with: homelab stacks restart immich\n",
		"Fill in the missing values with: homelab stacks var set immich <NAME> --value-stdin\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStackRollbackSendsTheGenerationOnlyWhenGiven(t *testing.T) {
	stackOpPoll = time.Millisecond
	var bodies []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/stacks/immich/rollback", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, stackJSON(`{"kind":"rollback","phase":"done","started_at":"2026-11-15T22:00:00Z","finished_at":"2026-11-15T22:01:00Z"}`))
	})
	serve(t, mux)
	for _, args := range [][]string{{"stacks", "rollback", "immich"}, {"stacks", "rollback", "--generation", "3", "immich"}} {
		if _, err := captureStdout(t, func() error { return runApp(t, args...) }); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 2 || bodies[0] != "" || bodies[1] != `{"generation":3}` {
		t.Errorf("bodies = %q", bodies)
	}
}

func TestStackSourceSendsOnlyTheSource(t *testing.T) {
	var body string
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/stacks/immich", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	serve(t, mux)
	if _, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "source", "immich", "github:example/stack-immich")
	}); err != nil {
		t.Fatal(err)
	}
	if body != `{"source":"github:example/stack-immich"}` {
		t.Errorf("body = %s", body)
	}
	if err := runApp(t, "stacks", "source", "immich"); err == nil {
		t.Error("a missing source was accepted")
	}
}

// TestStackVarSetReadsTheValueFromStdinOnly follows the --value-stdin
// convention: at most one trailing LF or CRLF is dropped, every other byte is
// kept, and no TTY is needed (stdin is a pipe here). qui would take a trailing
// newline as part of its session secret.
func TestStackVarSetReadsTheValueFromStdinOnly(t *testing.T) {
	before := []string{"stacks", "var", "set", "--secret", "--value-stdin", "immich", "API_KEY"}
	// The documented form: urfave/cli stops at the first argument, so the
	// command takes its flags from after the arguments itself.
	after := []string{"stacks", "var", "set", "immich", "API_KEY", "--secret", "--value-stdin"}
	for _, tc := range []struct {
		name, in, want string
		argv           []string
	}{
		{"flags after the arguments", "s3cret\n", "s3cret", after},
		{"one LF dropped", "s3cret value\n", "s3cret value", nil},
		{"one CRLF dropped", "s3cret\r\n", "s3cret", nil},
		{"no newline kept as is", "s3cret", "s3cret", nil},
		{"only one of two LFs dropped", "s3cret\n\n", "s3cret\n", nil},
		{"inner newline and spaces kept", " a\nb \t", " a\nb \t", nil},
		{"lone CR kept", "s3cret\r", "s3cret\r", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var method, path string
			var got stacks.StackVariableSet
			mux := http.NewServeMux()
			mux.HandleFunc("PUT /api/stacks/immich/variables/API_KEY", func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, stackJSON("null"))
			})
			serve(t, mux)
			stdin, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stdin
			os.Stdin = stdin
			defer func() { os.Stdin = old }()
			_, _ = w.WriteString(tc.in)
			w.Close()
			if _, err := captureStdout(t, func() error {
				argv := tc.argv
				if argv == nil {
					argv = before
				}
				return runApp(t, argv...)
			}); err != nil {
				t.Fatal(err)
			}
			if method != http.MethodPut || path != "/api/stacks/immich/variables/API_KEY" || got.Secret == nil || !*got.Secret || got.Value != tc.want {
				t.Errorf("%s %s secret=%v value=%q, want %q", method, path, got.Secret, got.Value, tc.want)
			}
		})
	}
}

func TestStackVarListNeverShowsASecret(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	serve(t, mux)
	out, err := captureStdout(t, func() error { return runApp(t, "stacks", "var", "list", "immich") })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"NAME         KIND    REQUIRED  VALUE          NOTES\n",
		"DB_PASSWORD  secret  yes       (set)          Database password\n",
		"API_KEY      secret  yes       (not set)      \n",
		"MEDIA_DIR    path    yes       /pool/films    at /media, read-write; Library\n",
		"TZ           env     no        UTC (default)  \n",
		`GREETING     env     -         "two\nlines"   not declared; file only` + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestStackFolderAddKeepsTheOthers(t *testing.T) {
	var got stacks.StackFoldersSet
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	mux.HandleFunc("PUT /api/stacks/immich/folders", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	serve(t, mux)
	if _, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "folder", "add", "immich", "/pool/share", "/share", "--read-write")
	}); err != nil {
		t.Fatal(err)
	}
	want := []stacks.StackFolderIn{{Path: "/pool/music", Mount: "/music", ReadOnly: true}, {Path: "/pool/share", Mount: "/share"}}
	if fmt.Sprint(got.Folders) != fmt.Sprint(want) {
		t.Errorf("folders = %+v, want %+v", got.Folders, want)
	}
	if _, err := captureStdout(t, func() error { return runApp(t, "stacks", "folder", "rm", "immich", "/music") }); err != nil {
		t.Fatal(err)
	}
	if len(got.Folders) != 0 {
		t.Errorf("after rm: %+v", got.Folders)
	}
}

func TestStackFolderAddByName(t *testing.T) {
	var got stacks.StackFoldersSet
	hinted := strings.Replace(stackJSON("null"), `"health":{"units":[]}}`,
		`"health":{"units":[]},"added_folders":{"under":"/shares","label":"Shares","read_only":false}}`, -1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, hinted)
	})
	mux.HandleFunc("PUT /api/stacks/immich/folders", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, hinted)
	})
	serve(t, mux)
	if _, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "folder", "add", "immich", "/pool/photos", "Photos", "--read-write")
	}); err != nil {
		t.Fatal(err)
	}
	if n := len(got.Folders); n != 2 || got.Folders[1].Mount != "/shares/Photos" {
		t.Errorf("folders = %+v", got.Folders)
	}
	if _, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "folder", "add", "immich", "/pool/photos", "a/b")
	}); err == nil || !strings.Contains(err.Error(), "not a name for shares") {
		t.Errorf("a name with '/': %v", err)
	}
}

func TestStackFolderAddNameNeedsHint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	serve(t, mux)
	_, err := captureStdout(t, func() error { return runApp(t, "stacks", "folder", "add", "immich", "/pool/photos", "Photos") })
	if err == nil || !strings.Contains(err.Error(), "is not a mount") {
		t.Errorf("err = %v", err)
	}
}

func TestStackVarSetReadOnlyKeepsTheFolder(t *testing.T) {
	var got stacks.StackVariableSet
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	mux.HandleFunc("PUT /api/stacks/immich/variables/MEDIA_DIR", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	serve(t, mux)
	if _, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "var", "set", "immich", "MEDIA_DIR", "--read-only")
	}); err != nil {
		t.Fatal(err)
	}
	if got.Value != "/pool/films" || got.ReadOnly == nil || !*got.ReadOnly {
		t.Errorf("sent %+v", got)
	}
}

func TestStackStopForce(t *testing.T) {
	stackOpPoll = time.Millisecond
	for _, tc := range []struct {
		args      []string
		wantForce bool
	}{
		{[]string{"stacks", "stop", "immich"}, false},
		{[]string{"stacks", "stop", "immich", "--force"}, true},
		{[]string{"stacks", "stop", "--force", "immich"}, true},
	} {
		var body []byte
		var reads atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("POST /api/stacks/immich/stop", func(w http.ResponseWriter, r *http.Request) {
			var err error
			body, err = io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, stackJSON(`{"kind":"stop","phase":"stopping the container","started_at":"2026-11-15T22:00:00Z"}`))
		})
		mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if reads.Add(1) == 1 {
				fmt.Fprint(w, stackJSON(`{"kind":"stop","phase":"stopping the container","started_at":"2026-11-15T22:00:00Z"}`))
				return
			}
			fmt.Fprint(w, stackJSON(`{"kind":"stop","phase":"stopped","started_at":"2026-11-15T22:00:00Z","finished_at":"2026-11-15T22:00:05Z"}`))
		})
		serve(t, mux)
		out, err := captureStdout(t, func() error { return runApp(t, tc.args...) })
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if !strings.Contains(out, "stopped") {
			t.Errorf("%v: stdout = %q", tc.args, out)
		}
		var req stacks.StackStop
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("%v: unmarshal body %q: %v", tc.args, body, err)
			}
		}
		if req.Force != tc.wantForce {
			t.Errorf("%v: force = %v, want %v (body=%q)", tc.args, req.Force, tc.wantForce, body)
		}
	}
}

func TestStackRestartCommand(t *testing.T) {
	stackOpPoll = time.Millisecond
	var posted bool
	var reads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/stacks/immich/restart", func(w http.ResponseWriter, _ *http.Request) {
		posted = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, stackJSON(`{"kind":"restart","phase":"stopping the container","started_at":"2026-11-15T22:00:00Z"}`))
	})
	mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if reads.Add(1) == 1 {
			fmt.Fprint(w, stackJSON(`{"kind":"restart","phase":"waiting for it to be healthy","started_at":"2026-11-15T22:00:00Z"}`))
			return
		}
		fmt.Fprint(w, stackJSON(`{"kind":"restart","phase":"running generation 1 (abcdef1)","started_at":"2026-11-15T22:00:00Z","finished_at":"2026-11-15T22:00:05Z"}`))
	})
	serve(t, mux)
	out, err := captureStdout(t, func() error { return runApp(t, "stacks", "restart", "immich") })
	if err != nil {
		t.Fatal(err)
	}
	if !posted || !strings.Contains(out, "running generation 1") {
		t.Errorf("posted=%v stdout=%q", posted, out)
	}
}
