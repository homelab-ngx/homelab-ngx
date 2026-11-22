package drafts

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
)

// A draft is a whole repo: the manifest and flake.nix, and whatever else the
// module reads (more modules, a flake.lock, config files). The editor works
// on its text files; a binary or oversized file is listed and kept as it is.

const (
	// maxFile is the largest file the editor shows and a change may write.
	maxFile = 256 << 10
	// maxFiles and maxTotal bound a draft's repo.
	maxFiles = 500
	maxTotal = 16 << 20
	// maxPath is the longest path a change may name.
	maxPath = 256
)

// required are the files every draft has; neither can be deleted.
var required = []string{stacks.ManifestFile, stacks.FlakeFile}

// tree is a draft's files by path.
type tree map[string]DraftFile

// list is the tree as the API shows it, by path.
func (t tree) list() []DraftFile {
	out := make([]DraftFile, 0, len(t))
	for _, f := range t {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b DraftFile) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// text is a text file's content; "" for an absent or binary file.
func (t tree) text(p string) string {
	if f, ok := t[p]; ok && !f.Binary {
		return f.Content
	}
	return ""
}

func (t tree) clone() tree {
	out := make(tree, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}

// textFile is the API's view of a file with the given bytes.
func textFile(p string, b []byte) DraftFile {
	f := DraftFile{Path: p, Size: int64(len(b))}
	if len(b) > maxFile || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		f.Binary = true
		return f
	}
	f.Content = string(b)
	return f
}

// readTree reads the files of the repo in dir, leaving out .git. A symlink
// is listed as binary: it is kept, not followed.
func readTree(dir string) (tree, error) {
	t := tree{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			t[rel] = DraftFile{Path: rel, Size: info.Size(), Binary: true}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		t[rel] = textFile(rel, b)
		return nil
	})
	return t, err
}

// validPath refuses a path a change may not name: not relative and clean,
// outside the repo, inside .git, or with control characters.
func validPath(p string) error {
	bad := func(why string) error {
		return httpx.BadRequest("invalid_path", "%q %s", p, why)
	}
	switch {
	case p == "":
		return httpx.BadRequest("invalid_path", "a file needs a path")
	case len(p) > maxPath:
		return bad(fmt.Sprintf("is longer than %d characters", maxPath))
	case strings.HasPrefix(p, "/"):
		return bad("is not a path inside the repo; leave out the leading /")
	case strings.ContainsAny(p, "\\"):
		return bad("has a backslash; use / between folders")
	case path.Clean(p) != p:
		return bad("is not a plain path: no ./, .. or doubled /")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return bad("has a control character")
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return bad("leaves the repo")
		}
		if part == ".git" {
			return bad("is inside .git, which git keeps")
		}
	}
	return nil
}

// apply applies changes to t in order. It refuses a change it cannot make
// and a result over the repo's limits, leaving t as it was.
func (t tree) apply(changes []DraftChange) (tree, error) {
	next := t.clone()
	for _, c := range changes {
		if err := validPath(c.Path); err != nil {
			return nil, err
		}
		if c.Delete {
			if c.Content != nil {
				return nil, httpx.BadRequest("invalid_change", "%s: a change deletes a file or writes it, not both", c.Path)
			}
			if slices.Contains(required, c.Path) {
				return nil, httpx.BadRequest("required_file", "%s cannot be deleted: every stack has it", c.Path)
			}
			if _, ok := next[c.Path]; !ok {
				return nil, httpx.BadRequest("unknown_file", "there is no file %s to delete", c.Path)
			}
			delete(next, c.Path)
			continue
		}
		if c.Content == nil {
			return nil, httpx.BadRequest("invalid_change", "%s: give the file's content, or delete it", c.Path)
		}
		if len(*c.Content) > maxFile {
			return nil, httpx.BadRequest("file_too_large", "%s is larger than %d KiB", c.Path, maxFile>>10)
		}
		if !utf8.ValidString(*c.Content) {
			return nil, httpx.BadRequest("invalid_change", "%s is not UTF-8 text", c.Path)
		}
		if _, ok := next[c.Path]; !ok {
			if err := next.fits(c.Path); err != nil {
				return nil, err
			}
		}
		next[c.Path] = DraftFile{Path: c.Path, Size: int64(len(*c.Content)), Content: *c.Content}
	}
	if len(next) > maxFiles {
		return nil, httpx.BadRequest("too_many_files", "a draft holds at most %d files", maxFiles)
	}
	var total int64
	for _, f := range next {
		total += f.Size
	}
	if total > maxTotal {
		return nil, httpx.BadRequest("draft_too_large", "a draft holds at most %d MiB of files", maxTotal>>20)
	}
	return next, nil
}

// fits refuses a new file p that a file or folder of t is in the way of:
// a file where p needs a folder, or a folder at p.
func (t tree) fits(p string) error {
	for dir := path.Dir(p); dir != "."; dir = path.Dir(dir) {
		if _, ok := t[dir]; ok {
			return httpx.Conflict("path_taken", "%s is a file, so %s cannot be made", dir, p)
		}
	}
	for q := range t {
		if strings.HasPrefix(q, p+"/") {
			return httpx.Conflict("path_taken", "%s is a folder", p)
		}
	}
	return nil
}

// changed is the paths whose text differs between t and next, or that one
// of them lacks.
func (t tree) changed(next tree) []string {
	var out []string
	for p, f := range next {
		if g, ok := t[p]; !ok || g.Binary != f.Binary || g.Content != f.Content || g.Size != f.Size {
			out = append(out, p)
		}
	}
	for p := range t {
		if _, ok := next[p]; !ok {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// writeChanges makes dir hold what applying changes gives: deleted files
// removed with the folders they leave empty, written files made with their
// folders. A file that is not text and is not changed is never touched.
func writeChanges(dir string, changes []DraftChange) error {
	for _, c := range changes {
		full := filepath.Join(dir, filepath.FromSlash(c.Path))
		if c.Delete {
			if err := os.Remove(full); err != nil {
				return err
			}
			removeEmptyParents(dir, filepath.Dir(full))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		// A file that was a symlink is replaced, not written through.
		if fi, err := os.Lstat(full); err == nil && !fi.Mode().IsRegular() {
			if err := os.Remove(full); err != nil {
				return err
			}
		}
		if err := os.WriteFile(full, []byte(*c.Content), 0o640); err != nil {
			return err
		}
	}
	return nil
}

func removeEmptyParents(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return // not empty, or gone
		}
		dir = filepath.Dir(dir)
	}
}

// copyTree copies the files under src into dst, leaving out .git: files
// 0640 (0750 when executable) and folders 0750 whatever their mode was (a
// store path's are read only), symlinks as they are.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if d.Name() == ".git" && p != src {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o750)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.Type().IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			mode := os.FileMode(0o640)
			if info.Mode()&0o111 != 0 {
				mode = 0o750
			}
			return os.WriteFile(target, b, mode)
		default:
			return errors.New(p + " is neither a file, a folder nor a symlink")
		}
	})
}
