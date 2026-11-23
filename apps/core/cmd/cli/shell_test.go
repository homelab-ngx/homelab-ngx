package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestStackShellRefusesAStackItCannotEnter(t *testing.T) {
	for _, tt := range []struct{ from, to, want string }{
		{`"running":true`, `"running":false`, `stack "immich" is not running; start the stack first`},
		{`"running":true`, `"running":true,"frozen":true`, `stack "immich" is frozen; thaw it first`},
	} {
		t.Run(tt.to, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/stacks/immich", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, strings.Replace(stackJSON("null"), tt.from, tt.to, 1))
			})
			serve(t, mux)
			err := runApp(t, "stacks", "shell", "immich")
			if err == nil || err.Error() != tt.want {
				t.Errorf("err = %v; want %s", err, tt.want)
			}
		})
	}
}

func TestShellCommandTakesABareNameFromTheSystemProfile(t *testing.T) {
	t.Parallel()
	if got := shellCommand(nil); got != nil {
		t.Errorf("no command: %v", got)
	}
	if got := shellCommand([]string{"psql", "-U", "immich"}); !slices.Equal(got, []string{"/run/current-system/sw/bin/psql", "-U", "immich"}) {
		t.Errorf("bare name: %v", got)
	}
	if got := shellCommand([]string{"--", "psql"}); !slices.Equal(got, []string{"/run/current-system/sw/bin/psql"}) {
		t.Errorf("after --: %v", got)
	}
	if got := shellCommand([]string{"--"}); got != nil {
		t.Errorf("only --: %v", got)
	}
	if got := shellCommand([]string{"/bin/sh"}); !slices.Equal(got, []string{"/bin/sh"}) {
		t.Errorf("path: %v", got)
	}
}
