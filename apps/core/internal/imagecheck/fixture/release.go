package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Release is a release server as the release workflow lays one out, with
// SHA256SUMS signed by a key of its own. The guest reaches it at
// http://10.0.2.2:PORT (QEMU's user network maps that to the host).
type Release struct {
	Dir    string // what it serves
	GnuPG  string // the key's home
	Port   int
	srv    *http.Server
	ln     net.Listener
	Public []byte // the key, armored, for the guest's keyring
}

// NewRelease makes a key and starts serving dir on 127.0.0.1.
func NewRelease(dir string) (*Release, error) {
	r := &Release{Dir: filepath.Join(dir, "release"), GnuPG: filepath.Join(dir, "gnupg")}
	_ = os.RemoveAll(r.Dir)
	_ = os.RemoveAll(r.GnuPG)
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(r.GnuPG, 0o700); err != nil {
		return nil, err
	}
	if _, err := Run("", "gpg", "--homedir", r.GnuPG, "--batch", "--quiet", "--passphrase", "",
		"--quick-gen-key", "image check <check@example.com>", "ed25519", "sign", "never"); err != nil {
		return nil, err
	}
	pub, err := Run("", "gpg", "--homedir", r.GnuPG, "--armor", "--export")
	if err != nil {
		return nil, err
	}
	r.Public = []byte(pub)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r.ln = ln
	r.Port = ln.Addr().(*net.TCPAddr).Port
	r.srv = &http.Server{Handler: http.FileServer(http.Dir(r.Dir))}
	go r.srv.Serve(ln)
	return r, nil
}

// Close stops serving.
func (r *Release) Close() {
	if r.srv != nil {
		r.srv.Close()
	}
}

// Publish lays out version v's update files from out (the image's output
// folder) as a release, signs its sums and names it the latest. extra lines
// are appended to SHA256SUMS after it is signed (a tampered release).
func (r *Release) Publish(out, v string, tamper string) error {
	_ = os.RemoveAll(r.Dir)
	d := filepath.Join(r.Dir, v)
	if err := os.MkdirAll(filepath.Join(d, "core"), 0o755); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(out, "homelab_"+v+".root*.raw"))
	files = append(files, filepath.Join(out, "homelab_"+v+".efi"))
	sort.Strings(files)
	var names []string
	for _, f := range files {
		n := filepath.Base(f)
		if err := link(f, filepath.Join(d, n)); err != nil {
			return err
		}
		names = append(names, n)
	}
	core := "core/homelab-core_" + v + ".raw"
	if err := link(filepath.Join(out, core), filepath.Join(d, core)); err != nil {
		return err
	}
	names = append(names, core)
	var sums strings.Builder
	for _, n := range names {
		s, err := sum(filepath.Join(d, n))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", s, n)
	}
	if err := os.WriteFile(filepath.Join(d, "SHA256SUMS"), []byte(sums.String()), 0o644); err != nil {
		return err
	}
	if _, err := Run("", "gpg", "--homedir", r.GnuPG, "--batch", "--yes", "--detach-sign",
		"-o", filepath.Join(d, "SHA256SUMS.gpg"), filepath.Join(d, "SHA256SUMS")); err != nil {
		return err
	}
	if tamper != "" {
		f, err := os.OpenFile(filepath.Join(d, "SHA256SUMS"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		fmt.Fprintln(f, tamper)
		f.Close()
	}
	return os.WriteFile(filepath.Join(r.Dir, "latest"), []byte(v+"\n"), 0o644)
}

// link hard-links src at dst, or copies it where a link cannot be made.
func link(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Sum is a file's SHA-256, in hex.
func Sum(path string) (string, error) { return sum(path) }

// CoreNeeding makes out a copy of the core extension file core whose
// HOMELAB_MIN_IMAGE is need: a core for an image the host does not run.
func CoreNeeding(core, need, out, work string) error {
	dir := filepath.Join(work, "core")
	_ = os.RemoveAll(dir)
	defer os.RemoveAll(dir)
	if _, err := Run("", "fsck.erofs", "--extract="+dir, core); err != nil {
		return err
	}
	rel := filepath.Join(dir, "usr/lib/extension-release.d/extension-release.homelab-core")
	b, err := os.ReadFile(rel)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(l, "HOMELAB_MIN_IMAGE=") {
			lines[i] = "HOMELAB_MIN_IMAGE=" + need
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%s names no minimum image", core)
	}
	if err := os.WriteFile(rel, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return err
	}
	_ = os.Remove(out)
	_, err = Run("", "mkfs.erofs", "-zlz4hc", "--all-root", "-T", "1", out, dir)
	return err
}
