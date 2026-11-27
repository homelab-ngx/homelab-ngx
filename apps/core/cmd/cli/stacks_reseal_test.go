package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/stacks"
)

func testAgeKeysFile(t *testing.T, dir string) (string, string) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	text := "# created: 2026-01-01T00:00:00Z\n# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"
	p := filepath.Join(dir, "keys.txt")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, text
}

func TestStacksRestoreWithAgeKeyFlags(t *testing.T) {
	dir := t.TempDir()
	keyPath, keyText := testAgeKeysFile(t, dir)
	lockPath := filepath.Join(dir, "stacks.lock.json")
	if err := os.WriteFile(lockPath, []byte(`{"version":1,"stacks":[{"name":"immich","source":"github:example/stack-immich","rev":"aaaaaaa1","want_running":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var got []stacks.StackRestore
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/stacks-lock/restore", func(w http.ResponseWriter, r *http.Request) {
		var req stacks.StackRestore
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		got = append(got, req)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"restored":["immich"],"resealed":["immich","caddy"],"skipped":{}}`)
	})
	serve(t, mux)

	out, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "restore", lockPath, "--age-key-file", keyPath)
	})
	if err != nil {
		t.Fatalf("restore with file after path: %v", err)
	}
	if len(got) != 1 || got[0].UseHostLock || got[0].AgeKey != keyText || len(got[0].Stacks) != 1 {
		t.Fatalf("got[0] = %+v", got)
	}
	for _, want := range []string{"✔ immich restored", "✔ immich variables re-sealed", "✔ caddy variables re-sealed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Host lock without file argument and key via stdin.
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wPipe.WriteString(keyText); err != nil {
		t.Fatal(err)
	}
	_ = wPipe.Close()
	oldStdin := os.Stdin
	os.Stdin = rPipe
	t.Cleanup(func() { os.Stdin = oldStdin; _ = rPipe.Close() })

	if _, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "restore", "--age-key-stdin")
	}); err != nil {
		t.Fatalf("restore with stdin: %v", err)
	}
	if len(got) != 2 || !got[1].UseHostLock || got[1].AgeKey != keyText {
		t.Fatalf("got[1] = %+v", got[1])
	}

	// Reject both flags together (whether before or after positional arg).
	err = runApp(t, "stacks", "restore", lockPath, "--age-key-file", keyPath, "--age-key-stdin")
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected conflict error, got %v", err)
	}
}

func TestStacksVarResealAndKeyMismatchDisplay(t *testing.T) {
	dir := t.TempDir()
	keyPath, keyText := testAgeKeysFile(t, dir)

	var gotBody stacks.StackVariablesReseal
	var gotPath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/stacks/immich/variables/reseal", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, stackJSON("null"))
	})
	mux.HandleFunc("GET /api/stacks/locked", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		raw := strings.Replace(stackJSON("null"), `"name":"immich"`, `"name":"locked"`, 1)
		raw = strings.Replace(raw, `"variables_ready":false`, `"variables_ready":false,"variables_key_mismatch":true,"variables_error":"variables are sealed to another host's age key"`, 1)
		fmt.Fprint(w, raw)
	})
	serve(t, mux)

	if err := runApp(t, "stacks", "var", "reseal", "immich"); err == nil || !strings.Contains(err.Error(), "--age-key-file") {
		t.Fatalf("expected missing key flag error, got %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "var", "reseal", "immich", "--age-key-file="+keyPath)
	})
	if err != nil {
		t.Fatalf("reseal failed: %v", err)
	}
	if gotPath != "/api/stacks/immich/variables/reseal" || gotBody.AgeKey != keyText {
		t.Fatalf("request = %s %+v", gotPath, gotBody)
	}
	if !strings.Contains(out, "✔ Variables re-sealed for immich.") {
		t.Errorf("unexpected output: %s", out)
	}

	showOut, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "show", "locked")
	})
	if err != nil {
		t.Fatalf("show locked: %v", err)
	}
	if !strings.Contains(showOut, "another host's age key") || !strings.Contains(showOut, "homelab stacks var reseal locked --age-key-file") {
		t.Errorf("show output missing mismatch notice:\n%s", showOut)
	}
	if strings.Contains(showOut, "Fill in the missing values") {
		t.Errorf("show output falsely told operator values are missing:\n%s", showOut)
	}

	listOut, err := captureStdout(t, func() error {
		return runApp(t, "stacks", "var", "list", "locked")
	})
	if err != nil {
		t.Fatalf("var list locked: %v", err)
	}
	if !strings.Contains(listOut, "another host's age key") || !strings.Contains(listOut, "homelab stacks var reseal locked --age-key-file") {
		t.Errorf("var list output missing mismatch notice:\n%s", listOut)
	}
}
