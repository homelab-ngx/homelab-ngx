package bcachefs

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func TestParseFileOptions(t *testing.T) {
	if got := parseFileOptions("", []string{"/var/mnt/pool/media"}); len(got) != 0 {
		t.Fatalf("empty stdout = %+v", got)
	}

	single := "foreground_target\tssd\nbackground_target\thdd\npromote_target\t-\ndata_replicas\t1\n"
	gotSingle := parseFileOptions(single, []string{"/var/mnt/pool/media"})
	wantSingle := map[string]FileOptions{
		"/var/mnt/pool/media": {DataReplicas: 1, ForegroundTarget: "ssd", BackgroundTarget: "hdd"},
	}
	if !reflect.DeepEqual(gotSingle, wantSingle) {
		t.Fatalf("single = %+v, want %+v", gotSingle, wantSingle)
	}

	multi := "/var/mnt/pool/hot\tforeground_target\tssd\n" +
		"/var/mnt/pool/hot\tbackground_target\tssd\n" +
		"/var/mnt/pool/hot\tpromote_target\t-\n" +
		"/var/mnt/pool/cold\tdata_replicas\t1\n" +
		"/var/mnt/pool/cold\tforeground_target\thdd\n" +
		"/var/mnt/pool/cold\tbackground_target\thdd\n" +
		"/var/mnt/pool/cold\tpromote_target\tnone\n" +
		"/var/mnt/pool/default\tdata_replicas\t-\n"
	gotMulti := parseFileOptions(multi, []string{"/var/mnt/pool/hot", "/var/mnt/pool/cold", "/var/mnt/pool/default"})
	wantMulti := map[string]FileOptions{
		"/var/mnt/pool/hot":  {ForegroundTarget: "ssd", BackgroundTarget: "ssd"},
		"/var/mnt/pool/cold": {DataReplicas: 1, ForegroundTarget: "hdd", BackgroundTarget: "hdd", PromoteTarget: "none"},
	}
	if !reflect.DeepEqual(gotMulti, wantMulti) {
		t.Fatalf("multi = %+v, want %+v", gotMulti, wantMulti)
	}
}

func TestGetAndSetFileOptions(t *testing.T) {
	f := cmdrunnertest.NewFake()
	f.On("bcachefs", "get-file-option").Reply("/var/mnt/pool/cold\tpromote_target\tnone\n/var/mnt/pool/cold\tforeground_target\thdd\n/var/mnt/pool/cold\tbackground_target\thdd\n")
	f.On("bcachefs", "set-file-option").Reply("")
	c := New(f)
	ctx := context.Background()

	got, err := c.GetFileOptions(ctx, []string{"/var/mnt/pool/hot", "/var/mnt/pool/cold"})
	if err != nil {
		t.Fatal(err)
	}
	if got["/var/mnt/pool/cold"] != (FileOptions{ForegroundTarget: "hdd", BackgroundTarget: "hdd", PromoteTarget: "none"}) {
		t.Fatalf("got = %+v", got)
	}

	empty, err := c.GetFileOptions(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	if calls := f.CallsMatching("bcachefs", "get-file-option"); len(calls) != 1 || !slices.Equal(calls[0].Args, []string{"get-file-option", "/var/mnt/pool/hot", "/var/mnt/pool/cold"}) {
		t.Fatalf("get-file-option calls = %+v", calls)
	}

	// No-op sends no command.
	if err := c.SetFileOptions(ctx, "/var/mnt/pool/tmp", FileOptionChanges{}); err != nil {
		t.Fatal(err)
	}

	strPtr := func(s string) *string { return &s }
	intPtr := func(n int) *int { return &n }

	if err := c.SetFileOptions(ctx, "/var/mnt/pool/hot", FileOptionChanges{
		ForegroundTarget: strPtr("ssd"), BackgroundTarget: strPtr("ssd"), PromoteTarget: strPtr("ssd"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFileOptions(ctx, "/var/mnt/pool/cached", FileOptionChanges{
		ForegroundTarget: strPtr(""), BackgroundTarget: strPtr(""), PromoteTarget: strPtr(""),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFileOptions(ctx, "/var/mnt/pool/cold", FileOptionChanges{
		DataReplicas: intPtr(1), ForegroundTarget: strPtr("hdd"), BackgroundTarget: strPtr("hdd"), PromoteTarget: strPtr("none"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFileOptions(ctx, "/var/mnt/pool/cold", FileOptionChanges{DataReplicas: intPtr(0)}); err != nil {
		t.Fatal(err)
	}

	var gotSet []string
	for _, call := range f.CallsMatching("bcachefs", "set-file-option") {
		gotSet = append(gotSet, strings.Join(call.Args, " "))
	}
	wantSet := []string{
		"set-file-option --foreground_target=ssd --background_target=ssd --promote_target=ssd /var/mnt/pool/hot",
		"set-file-option --foreground_target=- --background_target=- --promote_target=- /var/mnt/pool/cached",
		"set-file-option --data_replicas=1 --foreground_target=hdd --background_target=hdd --promote_target=none /var/mnt/pool/cold",
		"set-file-option --data_replicas=- /var/mnt/pool/cold",
	}
	if !slices.Equal(gotSet, wantSet) {
		t.Fatalf("set-file-option:\n got %q\nwant %q", gotSet, wantSet)
	}
}
