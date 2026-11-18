package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"homelab-ngx.com/core/internal/logging"
)

// A module name outside the allowed set panics when the daemon starts, which
// only shows on the host; catch it here instead.
func TestLoggerModulesAreKnown(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`logging\.Logger\("([^"]+)"\)`)
	seen := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			seen++
			if !logging.Known(m[1]) {
				t.Errorf("%s logs under %q, which logging does not know", f, m[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("found no logging.Logger calls; the pattern is stale")
	}
}
