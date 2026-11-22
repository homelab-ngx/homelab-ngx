package drafts

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
)

// stackFilesServer serves the drafts operations over a fake stack
// "photos" whose commit is tree.
func stackFilesServer(t *testing.T, tree string) *httptest.Server {
	t.Helper()
	s, fs := newService(t)
	fs.tree = tree
	fs.list = []stacks.Stack{{Name: "photos", Source: "github:o/photos"}}
	mux := http.NewServeMux()
	NewHTTP(s).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "test", "1")})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func getFiles(t *testing.T, srv *httptest.Server, name string) (int, []byte) {
	t.Helper()
	res, err := http.Get(srv.URL + "/api/stacks/" + name + "/files")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, body
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStackFilesListsTheCommit(t *testing.T) {
	t.Parallel()
	tree := t.TempDir()
	writeFile(t, filepath.Join(tree, stacks.ManifestFile), []byte(`{"version": 9}`))
	writeFile(t, filepath.Join(tree, stacks.FlakeFile), []byte("{ }\n"))
	writeFile(t, filepath.Join(tree, "modules", "web.nix"), []byte("{ }\n"))
	writeFile(t, filepath.Join(tree, "logo.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00"))
	writeFile(t, filepath.Join(tree, ".git", "HEAD"), []byte("ref: refs/heads/main\n"))
	if err := os.Symlink("/etc/passwd", filepath.Join(tree, "passwd")); err != nil {
		t.Fatal(err)
	}
	srv := stackFilesServer(t, tree)

	status, body := getFiles(t, srv, "photos")
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, body)
	}
	var got StackFiles
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "github:o/photos" || got.Rev != "abcdef1234567" {
		t.Errorf("source %q rev %q", got.Source, got.Rev)
	}
	want := []DraftFile{
		{Path: stacks.FlakeFile, Size: 4, Content: "{ }\n"},
		{Path: stacks.ManifestFile, Size: 14, Content: `{"version": 9}`},
		{Path: "logo.png", Size: 10, Binary: true},
		{Path: "modules/web.nix", Size: 4, Content: "{ }\n"},
		{Path: "passwd", Binary: true},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files %+v, want %+v", got.Files, want)
	}
	for i, f := range got.Files {
		w := want[i]
		if w.Path == "passwd" {
			w.Size = f.Size // the link's own size
		}
		if f != w {
			t.Errorf("file %d = %+v, want %+v", i, f, w)
		}
	}
}

func TestStackFilesRefusals(t *testing.T) {
	t.Parallel()
	many := t.TempDir()
	for i := range maxFiles + 1 {
		writeFile(t, filepath.Join(many, "f", fmt.Sprintf("%03d.nix", i)), nil)
	}
	big := t.TempDir()
	chunk := []byte(strings.Repeat("x", maxFile))
	for i := range maxTotal/maxFile + 1 {
		writeFile(t, filepath.Join(big, "part", fmt.Sprintf("%02d.txt", i)), chunk)
	}

	for _, c := range []struct {
		name, tree, stack string
		status            int
		code, detail      string
	}{
		{"over 500 files", many, "photos", http.StatusUnprocessableEntity, "too_large", "501 files"},
		{"over 16 MiB of text", big, "photos", http.StatusUnprocessableEntity, "too_large", "16 MiB"},
		{"unknown stack", many, "nope", http.StatusNotFound, "unknown_stack", "nope"},
	} {
		t.Run(c.name, func(t *testing.T) {
			status, body := getFiles(t, stackFilesServer(t, c.tree), c.stack)
			if status != c.status || !strings.Contains(string(body), `"code":"`+c.code+`"`) || !strings.Contains(string(body), c.detail) {
				t.Errorf("%d %s, want %d %s with %q", status, body, c.status, c.code, c.detail)
			}
		})
	}
}
