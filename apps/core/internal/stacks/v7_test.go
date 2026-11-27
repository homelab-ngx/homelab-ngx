package stacks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

// v7Manifest is immich as a version 7 stack: its module on the daemon's base.
const v7Manifest = `{
  "version": 7,
  "name": "immich",
  "nixpkgs": "nixos-unstable",
  "routes": [{"host": "immich.example.com", "port": 2283}],
  "variables": [
    {"name": "LOG_LEVEL", "kind": "env", "enum": ["info", "debug"]},
    {"name": "DB_PASSWORD", "kind": "secret"}
  ],
  "health": {"units": ["immich-server.service"], "http": {"port": 2283, "path": "/api/server/ping"}}
}`

// newV7Runtime is newRuntime with immich at version 7.
func newV7Runtime(t *testing.T) (*Service, *fakeSource, *fakeHost) {
	t.Helper()
	svc, fs, h := newServiceOn(t)
	fs.set("aaaaaaa1", v7Manifest)
	if _, err := svc.Add(context.Background(), src, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Approve(context.Background(), "immich", "aaaaaaa1"); err != nil {
		t.Fatal(err)
	}
	return svc, fs, h
}

func TestV7BuildsTheModuleOnTheBase(t *testing.T) {
	t.Parallel()
	svc, _, h := newV7Runtime(t)
	unstable := fakeNixpkgs("nixos-unstable")
	s := finished(t, svc, svc.Start)

	builds := h.CallsMatching("nix", "--extra-experimental-features", "nix-command flakes", "build")
	if len(builds) != 1 {
		t.Fatalf("builds = %+v", builds)
	}
	args := builds[0].Args
	dir := filepath.Join(svc.host.BuildsDir, "immich")
	if args[len(args)-1] != "path:"+dir+"#"+WrapperSystem || !slices.Contains(args, "--no-write-lock-file") {
		t.Errorf("built %v", args)
	}
	flake, err := os.ReadFile(filepath.Join(dir, "flake.nix"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`inputs.stack.url = "github:o/r/aaaaaaa1";`,
		`inputs.nixpkgs.url = "github:NixOS/nixpkgs/` + unstable + `";`,
		`networking.hostName = "immich";`,
		`time.timeZone = lib.mkDefault "Europe/Stockholm";`,
		`stack.nixosModules.default`,
	} {
		if !strings.Contains(string(flake), want) {
			t.Errorf("wrapper flake lacks %s:\n%s", want, flake)
		}
	}
	if base, _ := os.ReadFile(filepath.Join(dir, BaseModuleFile)); string(base) != BaseModule {
		t.Error("the base module was not written next to the wrapper")
	}
	if !s.Running || s.NixpkgsRev != unstable || s.Generations[0].NixpkgsRev != unstable || s.UpdateState != UpToDate {
		t.Errorf("running=%v nixpkgs=%q generations=%+v state=%s", s.Running, s.NixpkgsRev, s.Generations, s.UpdateState)
	}
}

func TestAMovedChannelIsAnUpdate(t *testing.T) {
	t.Parallel()
	svc, _, h := newV7Runtime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)

	moved := strings.Repeat("b", 40)
	svc.resolveNixpkgs = func(context.Context, string) (string, error) { return moved, nil }
	// The same commit of the repo: only nixpkgs moved.
	if _, err := svc.CheckUpdates(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := svc.Get(ctx, "immich")
	if s.UpdateState != CommitsAvailable || s.NixpkgsRev != moved {
		t.Fatalf("state=%s nixpkgs=%s", s.UpdateState, s.NixpkgsRev)
	}
	h.mu.Lock()
	note := h.problems[updateScope+"immich"]
	h.mu.Unlock()
	if !strings.Contains(note, "nixpkgs moved to "+short(moved)) {
		t.Errorf("notification = %q", note)
	}

	s = finished(t, svc, svc.Update)
	if s.UpdateState != UpToDate || s.Generations[0].NixpkgsRev != moved {
		t.Errorf("after the update: state=%s generations=%+v", s.UpdateState, s.Generations)
	}
	if _, err := svc.Update(ctx, "immich"); err == nil {
		t.Error("an update with nothing new was not refused")
	}
}

func TestAFailedLookupKeepsTheNixpkgsItHad(t *testing.T) {
	t.Parallel()
	svc, _, _ := newV7Runtime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)
	svc.resolveNixpkgs = func(context.Context, string) (string, error) { return "", os.ErrDeadlineExceeded }
	if _, err := svc.Fetch(ctx, "immich"); err != nil {
		t.Fatal(err)
	}
	if s, _ := svc.Get(ctx, "immich"); s.NixpkgsRev != fakeNixpkgs("nixos-unstable") || s.UpdateState != UpToDate {
		t.Errorf("nixpkgs=%s state=%s", s.NixpkgsRev, s.UpdateState)
	}
}

func TestV7LockRestoresTheSameNixpkgs(t *testing.T) {
	t.Parallel()
	svc, _, _ := newV7Runtime(t)
	finished(t, svc, svc.Start)
	l, err := svc.CurrentLock(context.Background())
	if err != nil || len(l.Stacks) != 1 || !l.Stacks[0].WantRunning || l.Stacks[0].NixpkgsRev != fakeNixpkgs("nixos-unstable") {
		t.Fatalf("lock = %+v, %v", l, err)
	}

	other, _, h := newServiceOn(t)
	other.resolveNixpkgs = func(context.Context, string) (string, error) { return strings.Repeat("c", 40), nil }
	h.On("systemctl", "--machine=immich", "is-active").Reply("active\n")
	res, err := other.Restore(context.Background(), l, "")
	if err != nil || len(res.Restored) != 1 {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	s := waitIdle(t, other, "immich")
	if len(s.Generations) != 1 || s.Generations[0].NixpkgsRev != fakeNixpkgs("nixos-unstable") {
		t.Errorf("restored generations = %+v", s.Generations)
	}
}

func TestV7EnvFileHoldsNoSecrets(t *testing.T) {
	t.Parallel()
	vars := []storedVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "DB_PASSWORD", Value: "hunter2", Secret: true}}
	if got := envFile(vars, false); got != "LOG_LEVEL=debug\n" {
		t.Errorf("v7 env file = %q", got)
	}
	if got := envFile(vars, true); !strings.Contains(got, "DB_PASSWORD=hunter2") {
		t.Errorf("v6 env file = %q", got)
	}
	if secretsInEnv(Manifest{Version: 7}) || !secretsInEnv(Manifest{Version: 6}) {
		t.Error("secretsInEnv by version")
	}
}

func TestSetVariableRefusesAValueOutsideTheEnum(t *testing.T) {
	t.Parallel()
	svc, _, _ := newV7Runtime(t)
	ctx := context.Background()
	_, err := svc.SetVariable(ctx, "immich", "LOG_LEVEL", StackVariableSet{Value: "loud"})
	wantCode(t, err, 422, "invalid_value")
	s, err := svc.SetVariable(ctx, "immich", "LOG_LEVEL", StackVariableSet{Value: "debug"})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range s.Variables {
		if v.Name == "LOG_LEVEL" && (v.Value != "debug" || !slices.Equal(v.Enum, []string{"info", "debug"}) || !v.InEnv) {
			t.Errorf("LOG_LEVEL = %+v", v)
		}
		if v.Name == "DB_PASSWORD" && v.InEnv {
			t.Errorf("a version 7 secret is said to be in the env file: %+v", v)
		}
	}
}

func TestAV7CommitWithoutAFlakeIsRefused(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	fs.set("aaaaaaa1", v7Manifest)
	fs.f.NoFlake = true
	_, err := svc.Add(context.Background(), src, "")
	wantCode(t, err, 422, "invalid_manifest")
}

func TestACommitBeforeVersion7IsRefused(t *testing.T) {
	t.Parallel()
	svc, fs := newService(t)
	fs.set("aaaaaaa1", v1Manifest)
	_, err := svc.Add(context.Background(), src, "")
	wantCode(t, err, 422, "invalid_manifest")
}

func TestAStackApprovedBeforeVersion7IsNotBuilt(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	// A lock from before version 7 brings the old manifest back as approved.
	if _, err := svc.Restore(ctx, Lock{Version: LockVersion, Stacks: []LockedStack{{
		Name: "immich", Source: src, Rev: "aaaaaaa1", URL: "github:o/r/aaaaaaa1", Manifest: json.RawMessage(v1Manifest),
	}}}, ""); err != nil {
		t.Fatal(err)
	}
	if s := finished(t, svc, svc.Start); !strings.Contains(s.Operation.Error, "version 7") || s.Generation != 0 {
		t.Errorf("start = %+v generation=%d", s.Operation, s.Generation)
	}
}

func TestWrapperFiles(t *testing.T) {
	t.Parallel()
	rev := strings.Repeat("d", 40)
	files, err := Wrapper{Name: "ha", Ref: `git+file:///x?ref=a"b`, NixpkgsRev: rev}.Files()
	if err != nil {
		t.Fatal(err)
	}
	if f := files["flake.nix"]; !strings.Contains(f, `"git+file:///x?ref=a\"b"`) || !strings.Contains(f, "lib.mkDefault null") {
		t.Errorf("flake.nix:\n%s", f)
	}
	for _, bad := range []Wrapper{{Name: "Ha", Ref: "x", NixpkgsRev: rev}, {Name: "ha", Ref: "x", NixpkgsRev: "nixos-unstable"}, {Name: "ha", NixpkgsRev: rev}} {
		if _, err := bad.Files(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if nixString("a${b}\\") != `"a\${b}\\"` {
		t.Errorf("nixString = %s", nixString("a${b}\\"))
	}
}

// waitIdle waits for the stack's running operation, if any, to finish.
func waitIdle(t *testing.T, svc *Service, name string) Stack {
	t.Helper()
	svc.mu.Lock()
	o := svc.ops[name]
	svc.mu.Unlock()
	if o != nil {
		select {
		case <-o.done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not finish; phase %q", o.kind, o.phase)
		}
	}
	s, err := svc.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestChoosingNixpkgs(t *testing.T) {
	t.Parallel()
	svc, _, _ := newV7Runtime(t)
	ctx := context.Background()
	finished(t, svc, svc.Start)

	s, err := svc.SetNixpkgs(ctx, "immich", "nixos-26.11", false)
	if err != nil {
		t.Fatal(err)
	}
	stable := fakeNixpkgs("nixos-26.11")
	if s.Nixpkgs != "nixos-26.11" || s.NixpkgsRev != stable || s.UpdateState != CommitsAvailable {
		t.Fatalf("nixpkgs=%q rev=%s state=%s", s.Nixpkgs, s.NixpkgsRev, s.UpdateState)
	}
	// A fetch looks up the choice, not the manifest's.
	if s, _ = svc.Fetch(ctx, "immich"); s.NixpkgsRev != stable {
		t.Errorf("after a fetch: %s", s.NixpkgsRev)
	}
	if l, _ := svc.CurrentLock(ctx); l.Stacks[0].Nixpkgs != "nixos-26.11" || l.Stacks[0].NixpkgsRev != stable {
		t.Errorf("lock = %+v", l.Stacks[0])
	}
	if s = finished(t, svc, svc.Update); s.Generations[0].NixpkgsRev != stable || s.UpdateState != UpToDate {
		t.Errorf("after the update: %+v %s", s.Generations, s.UpdateState)
	}

	// Back to the recommendation.
	if s, err = svc.SetNixpkgs(ctx, "immich", "", false); err != nil || s.Nixpkgs != "" || s.NixpkgsRev != fakeNixpkgs("nixos-unstable") {
		t.Errorf("reset: %q %s %v", s.Nixpkgs, s.NixpkgsRev, err)
	}

	_, err = svc.SetNixpkgs(ctx, "immich", "master", false)
	wantCode(t, err, 400, "invalid_nixpkgs")
	svc.resolveNixpkgs = func(context.Context, string) (string, error) { return "", os.ErrNotExist }
	_, err = svc.SetNixpkgs(ctx, "immich", "nixos-99.11", false)
	wantCode(t, err, 502, "nixpkgs_lookup_failed")
	if s, _ = svc.Get(ctx, "immich"); s.Nixpkgs != "" {
		t.Errorf("a choice that could not be looked up was stored: %q", s.Nixpkgs)
	}
}

func TestGoingBackToAnOlderNixpkgsNeedsConsent(t *testing.T) {
	t.Parallel()
	svc, _, _ := newV7Runtime(t)
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	pinned := strings.Repeat("d", 40)
	older := strings.Repeat("a", 40)
	releases := map[string]NixpkgsRelease{
		fakeNixpkgs("nixos-unstable"): {"26.11", day(10)},
		// A stable branch moves as often as unstable, with older packages.
		fakeNixpkgs("nixos-26.05"): {"26.05", day(17)},
		pinned:                     {"26.11", day(12)},
		older:                      {"26.11", day(1)},
	}
	svc.nixpkgsRelease = func(_ context.Context, rev string) (NixpkgsRelease, error) {
		if n, ok := releases[rev]; ok {
			return n, nil
		}
		return NixpkgsRelease{}, os.ErrNotExist
	}
	// Running unstable of the 10th; a commit of the 12th is newer.
	finished(t, svc, svc.Start)
	if _, err := svc.SetNixpkgs(ctx, "immich", pinned, false); err != nil {
		t.Fatal(err)
	}
	if s := finished(t, svc, svc.Update); s.Generations[0].NixpkgsRev != pinned {
		t.Fatalf("generations = %+v", s.Generations)
	}

	for name, spec := range map[string]string{
		"an older release, though made later": "nixos-26.05",
		"the same release, made earlier":      older,
		"a recommendation that went back":     "",
	} {
		_, err := svc.SetNixpkgs(ctx, "immich", spec, false)
		wantCode(t, err, 409, "nixpkgs_downgrade")
		if err != nil && name == "an older release, though made later" && !strings.Contains(err.Error(), "NixOS 26.05 of 2026-11-17") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, _ := svc.Get(ctx, "immich"); s.Nixpkgs != pinned {
		t.Errorf("a refused choice was stored: %q", s.Nixpkgs)
	}

	s, err := svc.SetNixpkgs(ctx, "immich", "nixos-26.05", true)
	if err != nil || s.Nixpkgs != "nixos-26.05" {
		t.Fatalf("with consent: %q, %v", s.Nixpkgs, err)
	}
	// A release that cannot be read could be a downgrade.
	_, err = svc.SetNixpkgs(ctx, "immich", strings.Repeat("e", 40), false)
	wantCode(t, err, 502, "nixpkgs_lookup_failed")
}

func TestOlderNixpkgs(t *testing.T) {
	t.Parallel()
	at := func(v string, d int) NixpkgsRelease {
		return NixpkgsRelease{v, time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC)}
	}
	for _, tc := range []struct {
		a, b NixpkgsRelease
		want bool
	}{
		{at("26.05", 17), at("26.11", 1), true},
		{at("26.11", 1), at("26.05", 17), false},
		{at("25.11", 1), at("26.05", 1), true},
		{at("26.11", 1), at("26.11", 2), true},
		{at("26.11", 2), at("26.11", 2), false},
	} {
		if got := olderNixpkgs(tc.a, tc.b); got != tc.want {
			t.Errorf("olderNixpkgs(%v, %v) = %v", tc.a, tc.b, got)
		}
	}
}

func TestListChannelsOffersUnstableAndTheNewestReleases(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("git").Reply("a\trefs/heads/nixos-25.11\nb\trefs/heads/nixos-26.05\nc\trefs/heads/nixos-26.05-small\nd\trefs/heads/nixos-26.11\ne\trefs/heads/nixos-unstable\nf\trefs/heads/nixos-unstable-small\n")
	got, err := ListChannels(context.Background(), f)
	if err != nil || !slices.Equal(got, []string{"nixos-unstable", "nixos-26.11", "nixos-26.05"}) {
		t.Errorf("channels = %v, %v", got, err)
	}
}

func TestAChangedValueAsksForARestart(t *testing.T) {
	t.Parallel()
	svc, _, _ := newV7Runtime(t)
	ctx := context.Background()
	if s := finished(t, svc, svc.Start); s.RestartNeeded {
		t.Fatal("a freshly booted stack needs a restart")
	}
	s, err := svc.SetVariable(ctx, "immich", "LOG_LEVEL", StackVariableSet{Value: "debug"})
	if err != nil || !s.RestartNeeded {
		t.Fatalf("after a change: restart_needed=%v, %v", s.RestartNeeded, err)
	}
	// Setting it back to what the stack booted with needs nothing.
	if s, _ = svc.DeleteVariable(ctx, "immich", "LOG_LEVEL"); s.RestartNeeded {
		t.Error("a value back as it was still asks for a restart")
	}
	if _, err := svc.SetVariable(ctx, "immich", "LOG_LEVEL", StackVariableSet{Value: "info"}); err != nil {
		t.Fatal(err)
	}
	finished(t, svc, svc.Stop)
	if s = finished(t, svc, svc.Start); s.RestartNeeded {
		t.Error("a restart did not apply the change")
	}
	// The API token file is part of it too.
	if err := os.MkdirAll(svc.apiEnvDir("immich"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(svc.apiEnvFile("immich"), []byte("HOMELAB_API_TOKEN=new\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if s, _ = svc.Get(ctx, "immich"); !s.RestartNeeded {
		t.Error("a new API token does not ask for a restart")
	}
}

func TestTheAPITokenIsABuiltinVariable(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newServiceOn(t)
	ctx := context.Background()
	fs.set("aaaaaaa1", strings.Replace(v7Manifest, `"routes"`, `"api": ["read"], "routes"`, 1))
	if _, err := svc.Add(ctx, src, ""); err != nil {
		t.Fatal(err)
	}
	s, err := svc.Approve(ctx, "immich", "aaaaaaa1")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(s.Variables, func(v StackVariable) bool { return v.Name == BuiltinToken })
	if i < 0 || !s.Variables[i].Builtin || !s.Variables[i].Set || !s.Variables[i].Secret {
		t.Fatalf("variables = %+v", s.Variables)
	}
	_, err = svc.SetVariable(ctx, "immich", BuiltinToken, StackVariableSet{Value: "x"})
	wantCode(t, err, 409, "builtin_variable")
	_, err = svc.DeleteVariable(ctx, "immich", BuiltinToken)
	wantCode(t, err, 409, "builtin_variable")
}
