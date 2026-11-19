package stacks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubTokenIsTheFirstFileThatHasOne(t *testing.T) {
	dir := t.TempDir()
	empty, second := filepath.Join(dir, "empty"), filepath.Join(dir, "second")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("ghp_abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := githubToken([]string{filepath.Join(dir, "missing"), empty, second}); got != "ghp_abc" {
		t.Fatalf("token = %q", got)
	}
	if got := githubToken([]string{filepath.Join(dir, "missing")}); got != "" {
		t.Fatalf("token = %q, want none", got)
	}
	saved := GitHubTokenFiles
	t.Cleanup(func() { GitHubTokenFiles = saved })
	GitHubTokenFiles = []string{second}
	if got := nixEnv(); len(got) != 1 || got[0] != "NIX_CONFIG=access-tokens = github.com=ghp_abc" {
		t.Fatalf("nix env = %q", got)
	}
	GitHubTokenFiles = []string{empty}
	if got := nixEnv(); got != nil {
		t.Fatalf("nix env with no token = %q", got)
	}
}
