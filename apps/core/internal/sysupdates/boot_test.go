package sysupdates

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/notifications"
)

// bootHost adds the boot loader's variables and the boot check's files to
// an image host's temp root.
func bootHost(t *testing.T, root string) {
	t.Helper()
	old := []string{efiVars, bootCheckFile, coreFellBackFile, coreFailedFile, coreMinImageBin}
	efiVars, bootCheckFile, coreFellBackFile = filepath.Join(root, "efivars"), filepath.Join(root, "boot-check-failed"), filepath.Join(root, "core-fell-back")
	coreFailedFile, coreMinImageBin = filepath.Join(root, "core-failed"), filepath.Join(root, "core-min-image")
	t.Cleanup(func() {
		efiVars, bootCheckFile, coreFellBackFile, coreFailedFile, coreMinImageBin = old[0], old[1], old[2], old[3], old[4]
	})
	_ = os.MkdirAll(efiVars, 0o755)
}

func setLoaderVar(t *testing.T, name, value string) {
	t.Helper()
	b := []byte{6, 0, 0, 0}
	for _, c := range utf16.Encode([]rune(value + "\x00")) {
		b = append(b, byte(c), byte(c>>8))
	}
	if err := os.WriteFile(filepath.Join(efiVars, name+"-"+loaderGUID), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBootImage(t *testing.T) {
	imageHost(t, "2")
	touch(t, filepath.Join(imageESPs[0], "homelab_2.efi"), filepath.Join(imageESPs[0], "homelab_1.efi"), filepath.Join(imageESPs[0], "homelab_3+0-3.efi"))
	f := cmdrunnertest.NewFake()
	f.On("bootctl", "set-oneshot", "homelab_1.efi").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		setLoaderVar(t, "LoaderEntryOneShot", "homelab_1.efi")
		return cmdrunner.Result{}, nil
	})
	f.On("bootctl", "set-default").Reply("")
	m := &Module{image: true, r: f, now: time.Now}
	ctx := context.Background()

	st, err := m.BootImage(ctx, "1", "once", false)
	if err != nil || st.Oneshot != "1" || st.Default != "" {
		t.Fatalf("once: %+v %v", st, err)
	}
	// A version that failed its boots can be tried again; the newest comes
	// back with an empty default.
	if _, err := m.BootImage(ctx, "3", "default", false); err != nil {
		t.Fatalf("a bad version: %v", err)
	}
	if _, err := m.BootImage(ctx, "", "default", false); err != nil || len(f.CallsMatching("bootctl", "set-default", "")) != 1 {
		t.Fatalf("clearing the default: %v", err)
	}
	if _, err := m.BootImage(ctx, "9", "once", false); !errors.Is(err, ErrNoSuchVersion) {
		t.Errorf("a version that is not installed: %v", err)
	}
	for _, c := range [][2]string{{"1", "always"}, {"", "once"}} {
		if _, err := m.BootImage(ctx, c[0], c[1], false); !errors.Is(err, ErrBadBootMode) {
			t.Errorf("version %q mode %q: %v", c[0], c[1], err)
		}
	}
	if _, err := (&Module{}).BootImage(ctx, "1", "once", false); !errors.Is(err, ErrNotImageHost) {
		t.Errorf("off the image: %v", err)
	}
}

func TestBootCheckNotes(t *testing.T) {
	imageHost(t, "2")
	touch(t, filepath.Join(coreExtDir, "homelab-core_1.raw"), filepath.Join(coreExtDir, "homelab-core_2.raw"))
	useCore(t, "1")
	_ = os.WriteFile(bootCheckFile, []byte("the daemon does not answer\n"), 0o644)
	_ = os.WriteFile(coreFellBackFile, []byte(`{"from":"2","to":"1"}`+"\n"), 0o644)
	n := &fakeNotifier{}
	m := &Module{image: true, notify: n, dir: t.TempDir(), now: time.Now}
	st, err := m.ImageStatus(context.Background())
	if err != nil || st.BootCheck != "the daemon does not answer" || st.Core.FellBackFrom != "2" {
		t.Fatalf("status = %+v %v", st, err)
	}
	m.notifyBoot(context.Background())
	if len(n.current) != 2 || n.current[0].Key != "image-boot:check" || n.current[1].Key != "image-boot:core:2" {
		t.Fatalf("notified %+v", n.current)
	}
	// A core update moved on: the note is gone, and so is the notification.
	useCore(t, "2")
	_ = os.Remove(bootCheckFile)
	m.notifyBoot(context.Background())
	if _, err := os.Stat(coreFellBackFile); len(n.current) != 0 || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after the update: notified %+v, note %v", n.current, err)
	}
}

func TestInWindow(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", "2026-11-19 "+s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		now, at string
		want    bool
	}{
		{"03:30", "03:30", true}, {"04:29", "03:30", true}, {"04:30", "03:30", false}, {"03:29", "03:30", false},
		{"00:10", "23:30", true}, {"00:30", "23:30", false}, {"12:00", "", false},
	} {
		if got := inWindow(at(c.now), c.at, time.Hour); got != c.want {
			t.Errorf("now %s, at %q = %v", c.now, c.at, got)
		}
	}
}

// A staged release installs at the set time, once per version.
func TestAutoInstall(t *testing.T) {
	imageHost(t, "1")
	touch(t, filepath.Join(imageESPs[0], "homelab_1.efi"), filepath.Join(imageUpdates, "homelab_2.efi"))
	f := cmdrunnertest.NewFake()
	f.On("systemctl", "is-active", deployUnit).Reply("inactive\n")
	f.On("systemd-run").Reply("")
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)
	m := &Module{image: true, r: f, dir: t.TempDir(), now: func() time.Time { return now }}
	ctx := context.Background()
	if _, err := m.SetAutoInstall(ctx, "25:00"); !errors.Is(err, ErrBadTime) {
		t.Fatalf("a bad time: %v", err)
	}
	m.autoInstall(ctx)
	if st, err := m.SetAutoInstall(ctx, "03:30"); err != nil || st.AutoInstallAt != "03:30" {
		t.Fatalf("set: %+v %v", st, err)
	}
	m.autoInstall(ctx)
	if n := len(f.CallsMatching("systemd-run")); n != 0 {
		t.Fatalf("installed outside its time (%d starts)", n)
	}
	now = time.Date(2026, 11, 20, 3, 40, 0, 0, time.Local)
	m.autoInstall(ctx)
	m.autoInstall(ctx)
	calls := f.CallsMatching("systemd-run")
	if len(calls) != 1 {
		t.Fatalf("%d starts, want one", len(calls))
	}
	if args := strings.Join(calls[0].Args, " "); !strings.Contains(args, "--kind install") || !strings.HasSuffix(args, "--reboot now") {
		t.Fatalf("started with %q", args)
	}
}

// The image goes in first, then the core, then the reboot. A reboot now
// leaves the core for the new image's boot.
func TestInstallRelease(t *testing.T) {
	imageHost(t, "1")
	touch(t, filepath.Join(imageESPs[0], "homelab_1.efi"), filepath.Join(coreExtDir, "homelab-core_1.raw"))
	useCore(t, "1")
	setLoaderVar(t, "LoaderEntryDefault", "homelab_1.efi")
	f := cmdrunnertest.NewFake()
	var order []string
	f.On(sysupdateBin, "--verify=no", "update").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		order = append(order, "image")
		touch(t, filepath.Join(imageESPs[0], "homelab_2+3-0.efi"))
		return cmdrunner.Result{}, nil
	})
	f.On(sysupdateBin, "--component=homelab-core").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		order = append(order, "core")
		touch(t, filepath.Join(coreExtDir, "homelab-core_2.raw"))
		useCore(t, "2")
		return cmdrunner.Result{}, nil
	})
	f.On("bootctl", "set-default", "").Reply("")
	f.On("systemd-sysext", "refresh").Reply("")
	f.On("systemctl", "restart").Reply("")
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	f.On("systemctl", "reboot").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		order = append(order, "reboot")
		return cmdrunner.Result{}, nil
	})
	x := newRun(t, f, KindInstall, func() error { return nil })
	x.o.Reboot = "now"
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := record(t, x); d.Result != DeployHealthy || d.Version != "2" || activeCore() != "1" || strings.Join(order, " ") != "image reboot" {
		t.Fatalf("record = %+v, core %s, order %v", d, activeCore(), order)
	}
	if len(f.CallsMatching("bootctl", "set-default", "")) != 1 {
		t.Fatal("a default set by hand was not cleared for the new image")
	}
	if n := len(f.CallsMatching("systemctl", "restart")); n != 0 {
		t.Fatalf("the daemon restarted %d times before the reboot", n)
	}

	// Without a reboot the core is applied now, and checked.
	imageHost(t, "1")
	touch(t, filepath.Join(imageESPs[0], "homelab_1.efi"), filepath.Join(coreExtDir, "homelab-core_1.raw"))
	useCore(t, "1")
	order = nil
	x = newRun(t, f, KindInstall, func() error { return nil })
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := record(t, x); d.Result != DeployHealthy || d.Version != "2" || activeCore() != "2" || strings.Join(order, " ") != "image core" {
		t.Fatalf("record = %+v, core %s, order %v", d, activeCore(), order)
	}

	// A core that does not start goes back, and the host is not rebooted
	// into the new image at the time asked for.
	imageHost(t, "1")
	touch(t, filepath.Join(imageESPs[0], "homelab_1.efi"), filepath.Join(coreExtDir, "homelab-core_1.raw"))
	useCore(t, "1")
	order = nil
	x = newRun(t, f, KindInstall, func() error {
		if activeCore() == "2" {
			return errors.New("no socket")
		}
		return nil
	})
	x.o.Reboot = "23:00"
	if err := x.run(context.Background()); err == nil {
		t.Fatal("an install whose core went back reported success")
	}
	if d := record(t, x); d.Result != DeployRolledBack || activeCore() != "1" || !strings.Contains(d.Reason, "image 2 is installed") || len(f.CallsMatching("shutdown")) != 0 {
		t.Fatalf("record = %+v, core %s, order %v", d, activeCore(), order)
	}
}

func TestFetchFileResumes(t *testing.T) {
	content := bytes.Repeat([]byte("homelab "), 4096)
	h := sha256.Sum256(content)
	sum := hex.EncodeToString(h[:])
	var ranges []string
	ranged := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		if !ranged {
			_, _ = w.Write(content)
			return
		}
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(content))
	}))
	defer srv.Close()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "f")
	check := func(what string) {
		t.Helper()
		n, err := fetchFile(ctx, srv.URL, path, sum)
		got, _ := os.ReadFile(path)
		if err != nil || n != int64(len(content)) || !bytes.Equal(got, content) {
			t.Fatalf("%s: %d bytes, %v", what, n, err)
		}
	}
	// Half a file from a try before: the rest is asked for.
	_ = os.WriteFile(path, content[:10000], 0o644)
	check("resumed")
	if ranges[len(ranges)-1] != "bytes=10000-" {
		t.Fatalf("asked for %q", ranges)
	}
	// The whole file is here already.
	check("complete")
	// A server without ranges sends it all again.
	ranged = false
	_ = os.WriteFile(path, content[:10000], 0o644)
	check("no ranges")
	// What is here is not the file's start: the sum fails and it is removed.
	ranged = true
	_ = os.WriteFile(path, bytes.Repeat([]byte("x"), 10000), 0o644)
	if _, err := fetchFile(ctx, srv.URL, path, sum); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a wrong start: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the file that failed its sum was kept")
	}
	check("afresh")
}

func TestRoomFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(make([]byte, 3<<20)))
	}))
	defer srv.Close()
	old := freeSpace
	t.Cleanup(func() { freeSpace = old })
	dir := t.TempDir()
	have := filepath.Join(dir, "have")
	_ = os.WriteFile(have, make([]byte, 1<<20), 0o644)
	files := map[string]string{srv.URL + "/a": filepath.Join(dir, "a"), srv.URL + "/b": have}
	// 3 MB and the 2 MB left of the other, plus the reserve.
	freeSpace = func(string) (uint64, error) { return downloadReserve + 5<<20, nil }
	if err := roomFor(context.Background(), dir, files); err != nil {
		t.Fatalf("enough room: %v", err)
	}
	freeSpace = func(string) (uint64, error) { return downloadReserve + 5<<20 - 1, nil }
	if err := roomFor(context.Background(), dir, files); err == nil || !strings.Contains(err.Error(), "not enough room") {
		t.Fatalf("too little room: %v", err)
	}
}

type fakeNotifier struct {
	scope   string
	current []notifications.Problem
}

func (f *fakeNotifier) Reconcile(_ context.Context, scope string, current []notifications.Problem) (notifications.Result, error) {
	f.scope, f.current = scope, current
	return notifications.Result{}, nil
}
