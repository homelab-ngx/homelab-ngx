package stacks

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestAddedFoldersAreBoundAfterTheManifestsAndYieldToIt(t *testing.T) {
	t.Parallel()
	m := mustParse(t, v5Manifest(photosPath))
	folders := []storedFolder{
		{Host: "/pool/music", Mount: "/music", ReadOnly: true},
		{Host: "/pool/share", Mount: "/share"},
		{Host: "/pool/other", Mount: "/pool/immich/other", ReadOnly: true}, // under PHOTOS: left out
	}
	b, err := hostBinds(m, nil, folders)
	if err != nil {
		t.Fatal(err)
	}
	a := nspawnArgs("immich", "/state/immich", "/profiles/immich", "/vars/immich", m, b)
	for _, want := range []string{"--bind=/pool/immich:/pool/immich:idmap", "--bind-ro=/pool/music:/music:idmap", "--bind=/pool/share:/share:idmap"} {
		if !slices.Contains(a, want) {
			t.Errorf("no %s in %v", want, a)
		}
	}
	if slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, "/pool/other") }) {
		t.Errorf("a shadowed folder is bound: %v", a)
	}
	if got := lenientBinds(m, nil, folders); len(got) != 3 {
		t.Errorf("lenientBinds = %v", got)
	}
}

func TestTheOperatorNarrowsADeclaredPathToReadOnly(t *testing.T) {
	t.Parallel()
	m := mustParse(t, v5Manifest(photosPath))
	b, err := hostBinds(m, []storedVar{{Name: "PHOTOS", Value: "/pool/immich", ReadOnly: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := nspawnArgs("immich", "/s", "/p", "/v", m, b); !slices.Contains(a, "--bind-ro=/pool/immich:/pool/immich:idmap") {
		t.Errorf("args = %v", a)
	}
}

func TestSetFolders(t *testing.T) {
	t.Parallel()
	svc, fs, h := newRuntime(t)
	ctx := context.Background()
	svc.statDir = func(string) error { return nil }
	release(t, svc, fs, "bbbbbbb2", v7Vars(photosPath))
	s := finished(t, svc, svc.Start)
	if s.RestartNeeded || len(s.Folders) != 0 {
		t.Fatalf("restart=%v folders=%v", s.RestartNeeded, s.Folders)
	}

	for _, tc := range []struct {
		in   StackFolderIn
		code string
	}{
		{StackFolderIn{Path: "/pool/music", Mount: "/pool/immich"}, "mount_taken"},
		{StackFolderIn{Path: "/pool/music", Mount: "/pool"}, "mount_taken"},
		{StackFolderIn{Path: "/pool/music", Mount: "/run/secrets/x"}, "invalid_mount"},
		{StackFolderIn{Path: "/pool/music", Mount: "/var/lib/homelab-api"}, "mount_taken"},
		{StackFolderIn{Path: "/pool/music", Mount: "music"}, "invalid_mount"},
		{StackFolderIn{Path: "/etc/x", Mount: "/music"}, "invalid_path"},
	} {
		if _, err := svc.SetFolders(ctx, "immich", StackFoldersSet{Folders: []StackFolderIn{tc.in}}); problemCode(err) != tc.code {
			t.Errorf("%+v: err = %v, want %s", tc.in, err, tc.code)
		}
	}
	two := []StackFolderIn{{Path: "/pool/music", Mount: "/media"}, {Path: "/pool/films", Mount: "/media/films"}}
	if _, err := svc.SetFolders(ctx, "immich", StackFoldersSet{Folders: two}); problemCode(err) != "mount_taken" {
		t.Errorf("nested folders: %v", err)
	}

	s, err := svc.SetFolders(ctx, "immich", StackFoldersSet{Folders: []StackFolderIn{{Path: "/pool/music", Mount: "/music", ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Folders) != 1 || s.Folders[0].Path != "/pool/music" || !s.Folders[0].ReadOnly || s.Folders[0].ShadowedBy != "" {
		t.Errorf("folders = %+v", s.Folders)
	}
	if !s.RestartNeeded {
		t.Error("an added folder does not ask for a restart")
	}
	// The variables are kept beside the folders, and the folders beside a
	// changed variable.
	if s, err = svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/photos"}); err != nil || len(s.Folders) != 1 {
		t.Fatalf("folders=%v, %v", s.Folders, err)
	}
	if s.Variables[0].Value != "/pool/photos" {
		t.Errorf("PHOTOS = %q", s.Variables[0].Value)
	}
	s = finished(t, svc, svc.Stop)
	s = finished(t, svc, svc.Start)
	boots := bootsOf(h)
	if last := boots[len(boots)-1]; !strings.Contains(last, "--bind-ro=/pool/music:/music:idmap") || !strings.Contains(last, "--bind=/pool/photos:/pool/immich:idmap") {
		t.Errorf("boot = %s", last)
	}
	if s, err = svc.SetFolders(ctx, "immich", StackFoldersSet{Folders: []StackFolderIn{}}); err != nil || len(s.Folders) != 0 || !s.RestartNeeded {
		t.Errorf("cleared: folders=%v restart=%v, %v", s.Folders, s.RestartNeeded, err)
	}
}

func TestSetVariableReadOnly(t *testing.T) {
	t.Parallel()
	svc, fs, _ := newRuntime(t)
	ctx := context.Background()
	svc.statDir = func(string) error { return nil }
	ro := `{"name":"MUSIC","kind":"path","mount":"/music","read_only":true}`
	release(t, svc, fs, "bbbbbbb2", v7Vars(photosPath+","+ro+`,{"name":"TZ","kind":"env"}`))
	yes, no := true, false

	s, err := svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/immich", ReadOnly: &yes})
	if err != nil {
		t.Fatal(err)
	}
	photos := s.Variables[0]
	if !*photos.ReadOnly || !photos.ReadOnlyByOperator {
		t.Errorf("PHOTOS = %+v", photos)
	}
	// A new value keeps the narrowing; false lifts it.
	if s, err = svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/photos"}); err != nil || !s.Variables[0].ReadOnlyByOperator {
		t.Fatalf("kept: %+v, %v", s.Variables[0], err)
	}
	if s, err = svc.SetVariable(ctx, "immich", "PHOTOS", StackVariableSet{Value: "/pool/photos", ReadOnly: &no}); err != nil || *s.Variables[0].ReadOnly {
		t.Fatalf("lifted: %+v, %v", s.Variables[0], err)
	}

	if _, err := svc.SetVariable(ctx, "immich", "MUSIC", StackVariableSet{Value: "/pool/music", ReadOnly: &no}); problemCode(err) != "declared_read_only" {
		t.Errorf("MUSIC made writable: %v", err)
	}
	if s, err = svc.SetVariable(ctx, "immich", "MUSIC", StackVariableSet{Value: "/pool/music", ReadOnly: &yes}); err != nil || s.Variables[1].ReadOnlyByOperator {
		t.Errorf("MUSIC: %+v, %v", s.Variables[1], err)
	}
	if _, err := svc.SetVariable(ctx, "immich", "TZ", StackVariableSet{Value: "UTC", ReadOnly: &yes}); problemCode(err) != "not_a_path" {
		t.Errorf("TZ read-only: %v", err)
	}
}
