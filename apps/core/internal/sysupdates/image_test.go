package sysupdates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func TestParseUKIs(t *testing.T) {
	got := parseUKIs([]string{"homelab_202611180900.efi", "homelab_202611190900+2-1.efi", "homelab_202611170900.efi", "homelab_202611200900+0-3.efi", "other.efi"}, "202611180900")
	want := []ImageVersion{
		{Version: "202611200900", State: "bad"},
		{Version: "202611190900", State: "pending", TriesLeft: 2},
		{Version: "202611180900", State: "booted"},
		{Version: "202611170900", State: "good"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// imageHost points the image paths at a temp root for one test (not
// parallel: they are package variables).
func imageHost(t *testing.T, booted string) string {
	t.Helper()
	root := t.TempDir()
	old := []any{imageUpdates, imageBuildDir, imageESPs, coreExtDir, coreExtLink, osRelease}
	imageUpdates = filepath.Join(root, "updates")
	imageBuildDir = filepath.Join(root, "build")
	imageESPs = []string{filepath.Join(root, "esp")}
	coreExtDir = filepath.Join(root, "extensions.d")
	coreExtLink = filepath.Join(root, "extensions", "homelab-core.raw")
	osRelease = filepath.Join(root, "os-release")
	t.Cleanup(func() {
		imageUpdates, imageBuildDir, imageESPs = old[0].(string), old[1].(string), old[2].([]string)
		coreExtDir, coreExtLink, osRelease = old[3].(string), old[4].(string), old[5].(string)
	})
	for _, d := range []string{imageUpdates + "/core", imageESPs[0], coreExtDir, filepath.Dir(coreExtLink)} {
		_ = os.MkdirAll(d, 0o755)
	}
	// The boot loader's variables and the boot check's files too: a test
	// must not read, or remove, the machine's own.
	bootHost(t, root)
	_ = os.WriteFile(osRelease, []byte("ID=arch\nIMAGE_ID=homelab\nIMAGE_VERSION="+booted+"\n"), 0o644)
	return root
}

func touch(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func useCore(t *testing.T, v string) {
	t.Helper()
	_ = os.Remove(coreExtLink)
	if err := os.Symlink("../extensions.d/homelab-core_"+v+".raw", coreExtLink); err != nil {
		t.Fatal(err)
	}
}

func TestImageStatus(t *testing.T) {
	imageHost(t, "2")
	touch(t, filepath.Join(imageESPs[0], "homelab_2.efi"), filepath.Join(imageESPs[0], "homelab_1.efi"),
		filepath.Join(imageESPs[0], "homelab_3+0-3.efi"),
		filepath.Join(imageUpdates, "homelab_3.efi"), filepath.Join(imageUpdates, "homelab_4.efi"),
		filepath.Join(coreExtDir, "homelab-core_2.raw"), filepath.Join(imageUpdates, "core", "homelab-core_2.raw"),
		filepath.Join(imageUpdates, "core", "homelab-core_4.raw"))
	useCore(t, "2")
	m := &Module{image: true}
	st, err := m.ImageStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Booted != "2" || st.FellBackFrom != "3" || st.PendingReboot || len(st.Available) != 1 || st.Available[0] != "4" {
		t.Fatalf("image = %+v", st)
	}
	if st.Core.Active != "2" || len(st.Core.Available) != 1 || st.Core.Available[0] != "4" {
		t.Fatalf("core = %+v", st.Core)
	}
	if _, err := (&Module{}).ImageStatus(context.Background()); !errors.Is(err, ErrNotImageHost) {
		t.Fatalf("off the image: %v", err)
	}
}

func TestStartImageRefuses(t *testing.T) {
	ctx := context.Background()
	img := &Module{image: true}
	if _, err := img.StartImage(ctx, KindImageUpdate, "tomorrow"); !errors.Is(err, ErrBadReboot) {
		t.Errorf("bad time: %v", err)
	}
	if _, err := img.StartImage(ctx, KindCoreUpdate, "now"); !errors.Is(err, ErrBadReboot) {
		t.Errorf("reboot on a core update: %v", err)
	}
	if _, err := img.StartImage(ctx, KindBuild, ""); !errors.Is(err, ErrDeployNotSet) {
		t.Errorf("build without a source: %v", err)
	}
	if _, err := (&Module{}).StartImage(ctx, KindCoreUpdate, ""); !errors.Is(err, ErrNotImageHost) {
		t.Errorf("image op off the image: %v", err)
	}
}

// sysupdateMoves makes the fake sysupdate merge core version v.
func sysupdateMoves(t *testing.T, f *cmdrunnertest.Fake, v string) {
	f.On(sysupdateBin, "--component=homelab-core").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		touch(t, filepath.Join(coreExtDir, "homelab-core_"+v+".raw"))
		useCore(t, v)
		return cmdrunner.Result{}, nil
	})
}

func TestCoreUpdateHealthy(t *testing.T) {
	imageHost(t, "1")
	touch(t, filepath.Join(coreExtDir, "homelab-core_1.raw"))
	useCore(t, "1")
	f := cmdrunnertest.NewFake()
	sysupdateMoves(t, f, "2")
	f.On("systemctl", "restart").Reply("")
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x := newRun(t, f, KindCoreUpdate, func() error { return nil })
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := record(t, x)
	if d.Result != DeployHealthy || d.Version != "2" || activeCore() != "2" {
		t.Fatalf("record = %+v, active %s", d, activeCore())
	}
	if len(f.CallsMatching("systemctl", "restart", "homelab-daemon.service", "caddy.service")) != 1 {
		t.Fatal("the daemon and Caddy were not restarted")
	}
}

// A core that does not come back is replaced by the one before.
func TestCoreUpdateRollsBack(t *testing.T) {
	imageHost(t, "1")
	touch(t, filepath.Join(coreExtDir, "homelab-core_1.raw"))
	useCore(t, "1")
	f := cmdrunnertest.NewFake()
	sysupdateMoves(t, f, "2")
	f.On("systemctl", "restart").Reply("")
	f.On("systemd-sysext", "refresh").Reply("")
	f.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	broken := true
	x := newRun(t, f, KindCoreUpdate, func() error {
		if broken && activeCore() == "2" {
			return errors.New("no socket")
		}
		return nil
	})
	if err := x.run(context.Background()); err == nil {
		t.Fatal("a rolled back update reported success")
	}
	if d := record(t, x); d.Result != DeployRolledBack || activeCore() != "1" {
		t.Fatalf("record = %+v, active %s", d, activeCore())
	}
	if len(f.CallsMatching("systemd-sysext", "refresh")) != 1 {
		t.Fatal("the extension was not merged again")
	}
	// No boot uses it again by itself; asked for again and healthy, it is
	// forgotten.
	if got := coreFailed(); !slices.Equal(got, []string{"2"}) {
		t.Fatalf("cores that did not start = %v", got)
	}
	broken = false
	x = newRun(t, f, KindCoreUpdate, func() error { return nil })
	if err := x.run(context.Background()); err != nil || activeCore() != "2" || len(coreFailed()) != 0 {
		t.Fatalf("the second try: %v, active %s, failed %v", err, activeCore(), coreFailed())
	}
}

// A core names the oldest image it runs on. On an older one it is refused,
// and an install of both leaves it for the new image's first boot.
func TestACoreThatNeedsANewerImageWaitsForIt(t *testing.T) {
	imageHost(t, "1")
	touch(t, filepath.Join(coreExtDir, "homelab-core_1.raw"), filepath.Join(imageUpdates, "core", "homelab-core_2.raw"),
		filepath.Join(imageESPs[0], "homelab_1.efi"), coreMinImageBin)
	useCore(t, "1")
	f := cmdrunnertest.NewFake()
	f.On(coreMinImageBin).Reply("2\n")
	x := newRun(t, f, KindCoreUpdate, func() error { return nil })
	if err := x.run(context.Background()); err == nil {
		t.Fatal("a core that needs a newer image was applied")
	}
	d := record(t, x)
	if d.Result != DeployFailed || !strings.Contains(d.Reason, "core 2 needs image 2 or newer") || activeCore() != "1" {
		t.Fatalf("record = %+v, active %s", d, activeCore())
	}
	if n := len(f.CallsMatching(sysupdateBin)); n != 0 {
		t.Fatalf("sysupdate ran %d times for a core that was refused", n)
	}

	// With its image: the image goes into the free slot, the core waits.
	f.On(sysupdateBin, "--verify=no", "update").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		touch(t, filepath.Join(imageESPs[0], "homelab_2+3-0.efi"))
		return cmdrunner.Result{}, nil
	})
	f.On("systemctl", "reboot").Reply("")
	x = newRun(t, f, KindInstall, func() error { return nil })
	x.o.Reboot = "now"
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := record(t, x); d.Result != DeployHealthy || d.Version != "2" || activeCore() != "1" || len(f.CallsMatching("systemctl", "reboot")) != 1 {
		t.Fatalf("record = %+v, active %s", d, activeCore())
	}
	if n := len(f.CallsMatching(sysupdateBin, "--component=homelab-core")); n != 0 {
		t.Fatal("the core was applied before its image started")
	}

	// A core without a minimum, or one the image meets, is applied.
	f2 := cmdrunnertest.NewFake()
	f2.On(coreMinImageBin).Reply("1\n")
	sysupdateMoves(t, f2, "2")
	f2.On("systemctl", "restart").Reply("")
	f2.On("systemctl", "is-active", "caddy.service").Reply("active\n")
	x = newRun(t, f2, KindCoreUpdate, func() error { return nil })
	if err := x.run(context.Background()); err != nil || activeCore() != "2" {
		t.Fatalf("a core the image meets: %v, active %s", err, activeCore())
	}
}

func TestImageUpdateReboots(t *testing.T) {
	imageHost(t, "1")
	touch(t, filepath.Join(imageESPs[0], "homelab_1.efi"))
	f := cmdrunnertest.NewFake()
	f.On(sysupdateBin, "--verify=no", "update").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		touch(t, filepath.Join(imageESPs[0], "homelab_2+3-0.efi"))
		return cmdrunner.Result{}, nil
	})
	f.On("systemctl", "reboot").Reply("")
	x := newRun(t, f, KindImageUpdate, func() error { return nil })
	x.o.Reboot = "now"
	if err := x.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := record(t, x); d.Result != DeployHealthy || d.Version != "2" || len(f.CallsMatching("systemctl", "reboot")) != 1 {
		t.Fatalf("record = %+v", d)
	}
}

func TestStage(t *testing.T) {
	imageHost(t, "1")
	out := filepath.Join(imageBuildDir, "out")
	_ = os.MkdirAll(filepath.Join(out, "core"), 0o755)
	// Dotted versions, in an order plain string comparison gets wrong.
	for _, v := range []string{"0.0.9", "0.0.10", "0.1.0"} {
		touch(t, filepath.Join(out, "homelab_"+v+".efi"), filepath.Join(out, "homelab_"+v+".root.aa.raw"),
			filepath.Join(out, "homelab_"+v+".root-verity.bb.raw"), filepath.Join(out, "core", "homelab-core_"+v+".raw"))
		if err := stage(out, v); err != nil {
			t.Fatal(err)
		}
	}
	if got := versionsIn(imageUpdates, stagedImageRE); len(got) != 2 || got[0] != "0.1.0" || got[1] != "0.0.10" {
		t.Fatalf("staged = %v", got)
	}
	if _, err := os.Stat(filepath.Join(imageUpdates, "homelab_0.0.9.root.aa.raw")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("version 0.0.9's root was not pruned")
	}
	if got := versionsIn(filepath.Join(imageUpdates, "core"), coreFileRE); len(got) != 2 {
		t.Fatalf("core staged = %v", got)
	}
}

// A release's version is a prefix of the builds of main that follow it.
// Pruning the release keeps those builds' files.
func TestStageKeepsBuildsAfterARelease(t *testing.T) {
	imageHost(t, "1")
	out := filepath.Join(imageBuildDir, "out")
	_ = os.MkdirAll(filepath.Join(out, "core"), 0o755)
	vs := []string{"0.3.2", "0.3.2.202611221827", "0.3.2.202611221934"}
	for _, v := range vs {
		touch(t, filepath.Join(out, "homelab_"+v+".efi"), filepath.Join(out, "homelab_"+v+".root.aa.raw"),
			filepath.Join(out, "homelab_"+v+".root-verity.bb.raw"), filepath.Join(out, "core", "homelab-core_"+v+".raw"))
		if err := stage(out, v); err != nil {
			t.Fatal(err)
		}
	}
	if got := versionsIn(imageUpdates, stagedImageRE); len(got) != 2 || got[0] != vs[2] || got[1] != vs[1] {
		t.Fatalf("staged = %v", got)
	}
	for _, v := range vs[1:] {
		for _, f := range []string{"homelab_" + v + ".efi", "homelab_" + v + ".root.aa.raw", "homelab_" + v + ".root-verity.bb.raw", "core/homelab-core_" + v + ".raw"} {
			if _, err := os.Stat(filepath.Join(imageUpdates, f)); err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
	}
	for _, f := range []string{"homelab_0.3.2.efi", "homelab_0.3.2.root.aa.raw", "homelab_0.3.2.root-verity.bb.raw", "core/homelab-core_0.3.2.raw"} {
		if _, err := os.Stat(filepath.Join(imageUpdates, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was not pruned", f)
		}
	}
}

func TestGitURL(t *testing.T) {
	for in, want := range map[string]string{
		"github:o/r":                    "https://github.com/o/r.git",
		"github:o/r/main":               "https://github.com/o/r.git",
		"git+file:///var/lib/src":       "/var/lib/src",
		"git+https://example.com/x.git": "https://example.com/x.git",
	} {
		if got, err := gitURL(in); err != nil || got != want {
			t.Errorf("%s = %q %v, want %q", in, got, err, want)
		}
	}
}
