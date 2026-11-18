package backups

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"homelab-ngx.com/core/backup-runner/runfile"
)

const rcloneSectionText = "[b2]\ntype = b2\naccount = 0012345\nkey = " + rcloneTok + "\n"

// noSecret fails if any of the test secrets shows in v's JSON.
func noSecret(t *testing.T, what string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{s3Secret, rcloneTok, resticPass, resticEnv} {
		if strings.Contains(string(b), s) {
			t.Errorf("%s carries the secret %q: %s", what, s, b)
		}
	}
}

func TestDestinationsNeverShowSecrets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	s3 := h.dest(s3Input("Bucket"))
	rc := h.dest(rcloneInput("B2", rcloneSectionText, "bucket/dir"))
	rs := h.dest(resticInput("Repo", "s3:https://host/bucket", resticPass))

	if s3.S3 == nil || s3.S3.Prefix != "host" || s3.S3.Bucket != "bucket" || !s3.DeleteRemoved || s3.Kind != KindS3 {
		t.Errorf("s3 = %+v %+v", s3, s3.S3)
	}
	if rc.Rclone == nil || rc.Rclone.Remote != "b2" || rc.Rclone.Type != "b2" || rc.Rclone.Path != "bucket/dir" {
		t.Errorf("rclone = %+v", rc.Rclone)
	}
	if rs.Restic == nil || rs.Restic.Repository != "s3:https://host/bucket" ||
		!reflect.DeepEqual(rs.Restic.EnvNames, []string{"AWS_SECRET_ACCESS_KEY"}) || rs.Keep != (Keep{Daily: 7, Weekly: 4}) {
		t.Errorf("restic = %+v %+v", rs, rs.Restic)
	}
	list, err := h.svc.Destinations(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %d %v", len(list), err)
	}
	noSecret(t, "the destination list", list)
	rows, _ := h.q.ListBackupDestinations(ctx)
	for _, r := range rows {
		noSecret(t, "the config column of "+r.Name, r.Config)
		for _, s := range []string{s3Secret, rcloneTok, resticPass, resticEnv} {
			if strings.Contains(string(r.Secret), s) {
				t.Errorf("the secret column of %s is not encrypted", r.Name)
			}
		}
	}
	// The same through the API.
	var viaAPI []Destination
	if code, _ := call(t, h, "GET", "/api/backup-destinations", "", &viaAPI); code != 200 || len(viaAPI) != 3 {
		t.Fatalf("GET = %d, %d destinations", code, len(viaAPI))
	}
	noSecret(t, "GET /backup-destinations", viaAPI)
}

func TestS3UpdateKeepsSecretWhenLeftEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	d := h.dest(s3Input("Bucket"))
	in := s3Input("Renamed")
	in.S3.SecretAccessKey, in.S3.Bucket, in.DeleteRemoved = "", "other", false
	got, err := h.svc.UpdateDestination(ctx, d.Id, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || got.S3.Bucket != "other" || got.DeleteRemoved {
		t.Errorf("after update = %+v %+v", got, got.S3)
	}
	row, _ := h.svc.destination(ctx, d.Id)
	spec, _, err := h.svc.destinationSpec(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec.Config, "secret_access_key = "+s3Secret+"\n") || spec.Remote != "dest:other/host" {
		t.Errorf("spec = %q remote %q", spec.Config, spec.Remote)
	}
	// A new key replaces it.
	in.S3.SecretAccessKey = "another"
	if _, err := h.svc.UpdateDestination(ctx, d.Id, in); err != nil {
		t.Fatal(err)
	}
	row, _ = h.svc.destination(ctx, d.Id)
	if spec, _, _ = h.svc.destinationSpec(row); !strings.Contains(spec.Config, "secret_access_key = another\n") {
		t.Errorf("new key not stored: %q", spec.Config)
	}
	// The kind is fixed.
	if _, err := h.svc.UpdateDestination(ctx, d.Id, rcloneInput("x", rcloneSectionText, "")); invalidCode(err) != "invalid_destination" {
		t.Errorf("kind change err = %v", err)
	}
	if _, err := h.svc.UpdateDestination(ctx, 999, in); !errors.Is(err, ErrUnknownDestination) {
		t.Errorf("unknown id err = %v", err)
	}
}

func TestRcloneSection(t *testing.T) {
	t.Parallel()
	name, typ, clean, err := rcloneSection("  [my remote]\r\n# a comment\r\n type = sftp \r\n\r\nhost=example.com\n")
	if err != nil || name != "my remote" || typ != "sftp" || clean != "[my remote]\ntype = sftp\nhost=example.com\n" {
		t.Fatalf("got %q %q %q %v", name, typ, clean, err)
	}
	for what, text := range map[string]string{
		"two sections": "[a]\ntype = b2\n[b]\ntype = s3\n",
		"no type":      "[a]\naccount = x\n",
		"no header":    "type = b2\n",
		"bad name":     "[a/b]\ntype = b2\n",
		"not a line":   "[a]\ntype = b2\nnonsense\n",
		"empty":        "",
	} {
		if _, _, _, err := rcloneSection(text); invalidCode(err) != "invalid_rclone_config" {
			t.Errorf("%s: err = %v", what, err)
		}
	}
}

func TestRcloneUpdateKeepsSection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	d := h.dest(rcloneInput("B2", rcloneSectionText, "bucket"))
	got, err := h.svc.UpdateDestination(ctx, d.Id, rcloneInput("B2", "", "bucket/sub"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Rclone.Path != "bucket/sub" || got.Rclone.Remote != "b2" {
		t.Errorf("rclone = %+v", got.Rclone)
	}
	row, _ := h.svc.destination(ctx, d.Id)
	spec, _, _ := h.svc.destinationSpec(row)
	if spec.Kind != runfile.KindRclone || spec.Remote != "b2:bucket/sub" || !strings.Contains(spec.Config, rcloneTok) {
		t.Errorf("spec = %+v", spec)
	}
	if _, err := h.svc.CreateDestination(ctx, rcloneInput("None", "", "x")); invalidCode(err) != "invalid_rclone_config" {
		t.Errorf("create without a config err = %v", err)
	}
}

func TestResticGeneratedPasswordMustBeConfirmed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	h.setState(succeed)
	created, err := h.svc.CreateDestination(ctx, resticInput("Repo", "/mnt/disk/repo", ""))
	if err != nil {
		t.Fatal(err)
	}
	pw := created.GeneratedPassword
	if len(pw) < 40 || created.Destination.PasswordConfirmed {
		t.Fatalf("created = %+v", created)
	}
	if list, _ := h.svc.Destinations(ctx); strings.Contains(mustJSON(t, list), pw) {
		t.Error("the generated password shows in the list")
	}
	b, err := h.svc.Create(ctx, BackupInput{Name: "B", DestinationIds: []int64{created.Destination.Id},
		Sources: []SourceInput{{Kind: SourcePath, Path: h.data}}})
	if err != nil {
		t.Fatal(err)
	}
	if b.Destinations[0].Ready {
		t.Error("an unconfirmed restic destination reads as ready")
	}
	if _, err := h.svc.Trigger(ctx, b.Id, TriggerManual); !errors.Is(err, ErrNotReady) {
		t.Fatalf("trigger before confirm = %v, want ErrNotReady", err)
	}
	if h.launchCount() != 0 {
		t.Fatal("a run was launched for an unconfirmed destination")
	}
	// Reveal needs the destination's name.
	if _, err := h.svc.RevealPassword(ctx, created.Destination.Id, "repo"); !errors.Is(err, ErrConfirm) {
		t.Errorf("reveal with the wrong name = %v", err)
	}
	if got, err := h.svc.RevealPassword(ctx, created.Destination.Id, " Repo "); err != nil || got != pw {
		t.Errorf("reveal = %q %v", got, err)
	}
	d, err := h.svc.ConfirmPassword(ctx, created.Destination.Id)
	if err != nil || !d.PasswordConfirmed {
		t.Fatalf("confirm = %+v %v", d, err)
	}
	if r := h.run(b.Id); r.Result != ResultSuccess {
		t.Fatalf("run after confirm = %+v", r)
	}
	spec := h.lastSpec().Destinations[0]
	if spec.Kind != runfile.KindRestic || !spec.Init || spec.Password != pw || spec.Repository != "/mnt/disk/repo" ||
		spec.Env["AWS_SECRET_ACCESS_KEY"] != resticEnv || spec.Keep != (runfile.Keep{Daily: 7, Weekly: 4}) {
		t.Errorf("spec destination = %+v", spec)
	}
	// An update keeps the password and the right to create the repository.
	in := resticInput("Repo", "/mnt/disk/repo2", "")
	in.Restic.Env = nil
	if _, err := h.svc.UpdateDestination(ctx, d.Id, in); err != nil {
		t.Fatal(err)
	}
	row, _ := h.svc.destination(ctx, d.Id)
	spec, write, _ := h.svc.destinationSpec(row)
	if !spec.Init || spec.Password != pw || spec.Env["AWS_SECRET_ACCESS_KEY"] != resticEnv || spec.Repository != "/mnt/disk/repo2" {
		t.Errorf("spec after update = %+v", spec)
	}
	if !reflect.DeepEqual(write, []string{"/mnt/disk/repo2"}) {
		t.Errorf("write paths = %v", write)
	}
	if row.PasswordConfirmedAt == nil {
		t.Error("an update dropped the confirmation")
	}
	in.Restic.Password = "new"
	if _, err := h.svc.UpdateDestination(ctx, d.Id, in); invalidCode(err) != "invalid_destination" {
		t.Errorf("password change err = %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestResticExistingRepositoryIsConfirmedAndNeverCreated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	created, err := h.svc.CreateDestination(ctx, resticInput("Repo", "b2:bucket:path", resticPass))
	if err != nil {
		t.Fatal(err)
	}
	if created.GeneratedPassword != "" || !created.Destination.PasswordConfirmed {
		t.Fatalf("created = %+v", created)
	}
	row, _ := h.svc.destination(ctx, created.Destination.Id)
	spec, write, err := h.svc.destinationSpec(row)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Init || spec.Password != resticPass || len(write) != 0 {
		t.Errorf("spec = %+v write %v", spec, write)
	}
	if _, err := h.svc.ConfirmPassword(ctx, h.dest(s3Input("S")).Id); invalidCode(err) != "invalid_destination" {
		t.Errorf("confirm on s3 err = %v", err)
	}
}

func TestDestinationValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	mod := func(in DestinationInput, f func(*DestinationInput)) DestinationInput { f(&in); return in }
	env := func(k, v string) *map[string]string { return &map[string]string{k: v} }
	cases := []struct {
		name string
		in   DestinationInput
		code string
	}{
		{"empty name", s3Input(" "), "invalid_name"},
		{"long name", s3Input(strings.Repeat("x", 65)), "invalid_name"},
		{"bad kind", DestinationInput{Name: "x", Kind: "ftp"}, "invalid_destination"},
		{"s3 without settings", DestinationInput{Name: "x", Kind: KindS3}, "invalid_destination"},
		{"endpoint not a url", mod(s3Input("x"), func(d *DestinationInput) { d.S3.Endpoint = "s3.example.com" }), "invalid_endpoint"},
		{"endpoint with credentials", mod(s3Input("x"), func(d *DestinationInput) { d.S3.Endpoint = "https://u:p@s3.example.com" }), "invalid_endpoint"},
		{"bucket with slash", mod(s3Input("x"), func(d *DestinationInput) { d.S3.Bucket = "a/b" }), "invalid_bucket"},
		{"no secret key", mod(s3Input("x"), func(d *DestinationInput) { d.S3.SecretAccessKey = "" }), "invalid_destination"},
		{"newline in key", mod(s3Input("x"), func(d *DestinationInput) { d.S3.SecretAccessKey = "a\nb = c" }), "invalid_destination"},
		{"rclone without settings", DestinationInput{Name: "x", Kind: KindRclone}, "invalid_destination"},
		{"restic without settings", DestinationInput{Name: "x", Kind: KindRestic}, "invalid_destination"},
		{"restic password in url", resticInput("x", "rest:https://user:pass@host/repo", ""), "invalid_repository"},
		{"restic empty repository", resticInput("x", " ", ""), "invalid_repository"},
		{"restic reserved credential", mod(resticInput("x", "/r", ""), func(d *DestinationInput) { d.Restic.Env = env("RESTIC_PASSWORD", "x") }), "invalid_credential"},
		{"restic lowercase credential", mod(resticInput("x", "/r", ""), func(d *DestinationInput) { d.Restic.Env = env("aws_key", "x") }), "invalid_credential"},
		{"restic multi-line credential", mod(resticInput("x", "/r", ""), func(d *DestinationInput) { d.Restic.Env = env("AWS_KEY", "a\nb") }), "invalid_credential"},
		{"restic keep too large", mod(resticInput("x", "/r", ""), func(d *DestinationInput) { d.Keep.Daily = 10000 }), "invalid_keep"},
	}
	for _, c := range cases {
		if _, err := h.svc.CreateDestination(ctx, c.in); invalidCode(err) != c.code {
			t.Errorf("%s: err = %v, want %s", c.name, err, c.code)
		}
	}
	if list, _ := h.svc.Destinations(ctx); len(list) != 0 {
		t.Errorf("a refused destination was stored: %+v", list)
	}
}

func TestDestinationNamesAndUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	b := h.backup("Nightly")
	d := b.Destinations[0]
	if _, err := h.svc.CreateDestination(ctx, s3Input("Nightly bucket")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name = %v", err)
	}
	other := h.dest(s3Input("Other"))
	if _, err := h.svc.UpdateDestination(ctx, other.Id, s3Input("Nightly bucket")); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename onto a taken name = %v", err)
	}
	list, _ := h.svc.Destinations(ctx)
	for _, x := range list {
		want := []string{}
		if x.Id == d.Id {
			want = []string{"Nightly"}
		}
		if !reflect.DeepEqual(x.UsedBy, want) {
			t.Errorf("%s used_by = %v, want %v", x.Name, x.UsedBy, want)
		}
	}
	if err := h.svc.DeleteDestination(ctx, d.Id); !errors.Is(err, ErrDestinationInUse) {
		t.Errorf("delete in use = %v", err)
	}
	if err := h.svc.DeleteDestination(ctx, other.Id); err != nil {
		t.Errorf("delete unused: %v", err)
	}
	if err := h.svc.DeleteDestination(ctx, other.Id); !errors.Is(err, ErrUnknownDestination) {
		t.Errorf("delete twice = %v", err)
	}
	// Once no backup copies to it, it can go.
	if _, err := h.svc.Update(ctx, b.Id, BackupPatch{DestinationIds: &[]int64{}}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteDestination(ctx, d.Id); err != nil {
		t.Errorf("delete after unlinking: %v", err)
	}
}

func TestDestinationSpecKinds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	spec := func(d Destination) (runfile.Destination, []string) {
		t.Helper()
		row, err := h.svc.destination(ctx, d.Id)
		if err != nil {
			t.Fatal(err)
		}
		s, w, err := h.svc.destinationSpec(row)
		if err != nil {
			t.Fatal(err)
		}
		return s, w
	}
	s3, w := spec(h.dest(s3Input("S3")))
	wantCfg := "[dest]\ntype = s3\nprovider = Other\nenv_auth = false\nno_check_bucket = true\n" +
		"access_key_id = AKID\nsecret_access_key = " + s3Secret + "\nendpoint = https://s3.example.com\nregion = eu\n"
	if s3.Kind != runfile.KindRclone || s3.Config != wantCfg || s3.Remote != "dest:bucket/host" || s3.Bucket != "dest:bucket" || !s3.DeleteRemoved || len(w) != 0 {
		t.Errorf("s3 spec = %+v write %v", s3, w)
	}
	noPrefix := s3Input("Root")
	noPrefix.S3.Prefix, noPrefix.S3.Region = "", ""
	if s, _ := spec(h.dest(noPrefix)); s.Remote != "dest:bucket" || s.Bucket != "dest:bucket" || strings.Contains(s.Config, "region") {
		t.Errorf("s3 without prefix = %q %q", s.Remote, s.Config)
	}
	local := filepath.Join(h.dir, "mirror")
	if s, w := spec(h.dest(rcloneInput("Disk", "[disk]\ntype = local\n", local))); s.Remote != "disk:"+local || !reflect.DeepEqual(w, []string{local}) {
		t.Errorf("local rclone = %q write %v", s.Remote, w)
	}
	if _, w := spec(h.dest(rcloneInput("B2", rcloneSectionText, "/bucket"))); len(w) != 0 {
		t.Errorf("a remote rclone destination got write paths %v", w)
	}
	if s, w := spec(h.dest(resticInput("Repo", "local:/mnt/usb/repo/", resticPass))); !reflect.DeepEqual(w, []string{"/mnt/usb/repo"}) || s.Kind != runfile.KindRestic {
		t.Errorf("local restic = %+v write %v", s, w)
	}
}

func TestTestDestination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	d := h.dest(resticInput("Repo", "/mnt/disk/repo", ""))
	answer := func(result, errText, note string) {
		h.setState(func(spec *runfile.Spec) *runfile.State {
			if !spec.CheckOnly || len(spec.Folders) != 0 || len(spec.Destinations) != 1 {
				t.Errorf("test spec = %+v", spec)
			}
			st := finalState(spec, result, runfile.ResultSuccess)
			st.Items[0].Result, st.Items[0].Error, st.Items[0].Note = result, errText, note
			return st
		})
	}
	answer(runfile.ResultSuccess, "", "the repository does not exist yet; the first run creates it")
	got, err := h.svc.TestDestination(ctx, d.Id)
	if err != nil || !got.Ok || got.Detail != "The repository does not exist yet; the first run creates it." {
		t.Fatalf("test = %+v %v", got, err)
	}
	h.mu.Lock()
	args := h.launches[len(h.launches)-1].Args
	h.mu.Unlock()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--wait") || !strings.Contains(joined, "ProtectSystem=strict") ||
		!strings.Contains(joined, "ReadWritePaths=-/mnt/disk/repo") || !strings.Contains(joined, "--unit=homelab-backup-test-") {
		t.Errorf("test launch args = %v", args)
	}
	answer(runfile.ResultFailure, "wrong password for this repository", "")
	if got, err := h.svc.TestDestination(ctx, d.Id); err != nil || got.Ok || got.Detail != "wrong password for this repository" {
		t.Errorf("failed test = %+v %v", got, err)
	}
	answer(runfile.ResultSuccess, "", "")
	if got, _ := h.svc.TestDestination(ctx, d.Id); !got.Ok || got.Detail != "The destination answered." {
		t.Errorf("plain success = %+v", got)
	}
	// The runner left nothing: not ok, never an error.
	h.setState(nil)
	if got, err := h.svc.TestDestination(ctx, d.Id); err != nil || got.Ok || got.Detail == "" {
		t.Errorf("no state = %+v %v", got, err)
	}
	if ents, _ := filepath.Glob(filepath.Join(h.dir, "run", "*")); len(ents) != 0 {
		t.Errorf("a test left files behind: %v", ents)
	}
	if _, err := h.svc.TestDestination(ctx, 999); !errors.Is(err, ErrUnknownDestination) {
		t.Errorf("unknown id = %v", err)
	}
	if runs, _ := h.q.RunningBackupRuns(ctx); len(runs) != 0 {
		t.Error("a destination test recorded a run")
	}
}

func TestDuplicateDestination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newHarness(t)
	src := h.dest(resticInput("Repo", "s3:https://host/bucket", resticPass))
	var names []string
	for range 3 {
		d, err := h.svc.DuplicateDestination(ctx, src.Id)
		if err != nil {
			t.Fatal(err)
		}
		noSecret(t, "the copy", d)
		names = append(names, d.Name)
	}
	if want := []string{"Repo-copy", "Repo-copy2", "Repo-copy3"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
	rows, _ := h.q.ListBackupDestinations(ctx)
	for _, r := range rows {
		if string(r.Secret) == "" || r.Kind != string(KindRestic) {
			t.Errorf("row %s = kind %s, secret %d bytes", r.Name, r.Kind, len(r.Secret))
		}
	}
	if _, err := h.svc.DuplicateDestination(ctx, 9999); !errors.Is(err, ErrUnknownDestination) {
		t.Errorf("unknown destination: %v", err)
	}
}
