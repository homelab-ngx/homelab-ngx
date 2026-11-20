package stacks

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// fakePlaces makes no folder: it says where one would be.
type fakePlaces struct {
	places []string
	made   []string
}

func (f *fakePlaces) FolderPlaces(context.Context) ([]string, error) { return f.places, nil }
func (f *fakePlaces) MakeFolder(_ context.Context, place, name string) (string, error) {
	f.made = append(f.made, place+"/"+name)
	return place + "/" + name, nil
}

// chowns records the owner changes the service asks for.
type chowns struct {
	mu    sync.Mutex
	calls []string
}

func (c *chowns) fn(_ context.Context, path string, uid, gid int, recursive bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, fmt.Sprintf("%s %d:%d recursive=%v", path, uid, gid, recursive))
	return nil
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors, ok := err.(*httpx.Problem); ok {
		p = errors
	}
	if p == nil {
		return fmt.Sprint(err)
	}
	return p.Code
}

func pathVar(t *testing.T, s Stack, name string) StackVariable {
	t.Helper()
	for _, v := range s.Variables {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no variable %s", name)
	return StackVariable{}
}

func TestStackIDsAreOnePerNameFromTheHostsRange(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		want uint32
	}{{"immich", 2000}, {"media", 2001}, {"immich", 2000}, {"jellyfin", 2002}, {"media", 2001}} {
		if got, err := svc.idFor(ctx, c.name); err != nil || got != c.want {
			t.Fatalf("idFor(%s) = %d, %v; want %d", c.name, got, err, c.want)
		}
	}
	// A pinned id is kept, one that is taken is refused, and so is one
	// outside the range.
	if _, err := svc.SetStackID(ctx, "arr", 2050); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.idFor(ctx, "arr"); got != 2050 {
		t.Fatalf("pinned id = %d", got)
	}
	if _, err := svc.SetStackID(ctx, "books", 2000); problemCode(err) != "id_taken" {
		t.Fatalf("a taken id: %v", err)
	}
	if _, err := svc.SetStackID(ctx, "books", 1999); problemCode(err) != "invalid_id" {
		t.Fatalf("an id below the range: %v", err)
	}
	// A full table refuses a new name and still knows the old ones.
	for id := int64(StackIDMin); id <= StackIDMax; id++ {
		_ = svc.st.Queries().UpsertStackID(ctx, store.UpsertStackIDParams{Name: fmt.Sprintf("n%d", id), ID: id, UpdatedAt: 1})
	}
	if _, err := svc.idFor(ctx, "one-more"); problemCode(err) != "stack_ids_full" {
		t.Fatalf("a full table: %v", err)
	}
}

func TestCreateFolderMakesItWithTheStackIDsAndSetsTheValue(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newV8Runtime(t)
	ctx := context.Background()
	places := &fakePlaces{places: []string{"/var/mnt/pool"}}
	var ch chowns
	svc.UsePlaces(places)
	svc.chown = ch.fn
	svc.statDir = func(string) error { return nil }

	s, err := svc.CreateFolder(ctx, "players", "MEDIA_DIR", StackFolderCreate{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(places.made, []string{"/var/mnt/pool/players"}) {
		t.Fatalf("made %v", places.made)
	}
	// jellyfin is the first name, media the second.
	if want := []string{"/var/mnt/pool/players 2000:2001 recursive=false"}; !reflect.DeepEqual(ch.calls, want) {
		t.Fatalf("chown = %v, want %v", ch.calls, want)
	}
	if v := pathVar(t, s, "MEDIA_DIR"); v.Value != "/var/mnt/pool/players" {
		t.Fatalf("value = %q", v.Value)
	}

	// More than one place: it has to be named. A name with a slash is refused.
	places.places = append(places.places, "/var/mnt/cache")
	if _, err := svc.CreateFolder(ctx, "players", "MEDIA_DIR", StackFolderCreate{}); problemCode(err) != "place_needed" {
		t.Fatalf("two places, none named: %v", err)
	}
	if _, err := svc.CreateFolder(ctx, "players", "MEDIA_DIR", StackFolderCreate{Place: "/var/mnt/cache", Folder: "a/b"}); problemCode(err) != "invalid_folder" {
		t.Fatalf("a path for a name: %v", err)
	}
	if _, err := svc.CreateFolder(ctx, "players", "MQTT_HOST", StackFolderCreate{}); problemCode(err) != "not_a_path" {
		t.Fatalf("a variable that is no path: %v", err)
	}
}

func TestAFoldersOwnerIsShownAgainstTheStackIDs(t *testing.T) {
	t.Parallel()
	svc, _, mu, owner, _ := newV8Runtime(t)
	ctx := context.Background()
	state := func() StackVariable {
		s, err := svc.Get(ctx, "players")
		if err != nil {
			t.Fatal(err)
		}
		return pathVar(t, s, "MEDIA_DIR")
	}
	// A restored library: ids of its own, which the stack takes. No stack
	// id is given to a name just for looking.
	v := state()
	if v.OwnerState != OwnerOwn || *v.FolderUID != 990 || *v.FolderGID != 169 || v.StackUID != nil {
		t.Fatalf("own ids: %+v", v)
	}
	// Owned by root: the stack cannot use it.
	mu.Lock()
	owner[0] = 0
	mu.Unlock()
	if v := state(); v.OwnerState != OwnerRoot {
		t.Fatalf("root: state = %q", v.OwnerState)
	}
	// On the stack ids.
	uid, _ := svc.idFor(ctx, "jellyfin")
	gid, _ := svc.idFor(ctx, "media")
	mu.Lock()
	owner[0], owner[1] = uid, gid
	mu.Unlock()
	if v := state(); v.OwnerState != OwnerOK || *v.StackUID != 2000 || *v.StackGID != 2001 {
		t.Fatalf("stack ids: %+v", v)
	}
	// Another stack's folder has another id for the same group.
	mu.Lock()
	owner[0], owner[1] = 990, 169
	mu.Unlock()
	if got := svc.otherGroupIDs(ctx, "music"); !reflect.DeepEqual(got["media"], []uint32{169}) {
		t.Fatalf("the other stacks' media ids = %v", got)
	}
}

func TestOwnFolderNeedsThePathAndAStoppedStack(t *testing.T) {
	t.Parallel()
	svc, _, _, _, _ := newV8Runtime(t)
	ctx := context.Background()
	var ch chowns
	svc.chown = ch.fn

	if _, err := svc.OwnFolder(ctx, "players", "MEDIA_DIR", StackFolderOwn{Confirm: "/pool"}); problemCode(err) != "confirm" {
		t.Fatalf("a wrong confirmation: %v", err)
	}
	finishedOn(t, svc, "players", svc.Start)
	if _, err := svc.OwnFolder(ctx, "players", "MEDIA_DIR", StackFolderOwn{Confirm: "/pool/media"}); problemCode(err) != "stack_running" {
		t.Fatalf("a running stack: %v", err)
	}
	if len(ch.calls) != 0 {
		t.Fatalf("an owner changed without leave: %v", ch.calls)
	}
	finishedOn(t, svc, "players", svc.Stop)
	if _, err := svc.OwnFolder(ctx, "players", "MEDIA_DIR", StackFolderOwn{Confirm: "/pool/media"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"/pool/media 2000:2001 recursive=true"}; !reflect.DeepEqual(ch.calls, want) {
		t.Fatalf("chown = %v, want %v", ch.calls, want)
	}
}

func TestAnEmptyRootOwnedFolderIsGivenTheStackIDs(t *testing.T) {
	t.Parallel()
	svc, _ := newService(t)
	ctx := context.Background()
	var ch chowns
	svc.chown = ch.fn
	d := Variable{Name: "MEDIA_DIR", Kind: VarPath, Owner: "jellyfin", Group: "media"}

	empty := t.TempDir()
	svc.statOwner = func(string) (uint32, uint32, error) { return 0, 0, nil }
	svc.adoptEmpty(ctx, d, empty)
	if want := []string{empty + " 2000:2001 recursive=false"}; !reflect.DeepEqual(ch.calls, want) {
		t.Fatalf("an empty root-owned folder: chown = %v, want %v", ch.calls, want)
	}

	// Only the group is root's: the user is left as it is.
	ch.calls = nil
	svc.statOwner = func(string) (uint32, uint32, error) { return 994, 0, nil }
	svc.adoptEmpty(ctx, d, empty)
	if want := []string{empty + " -1:2001 recursive=false"}; !reflect.DeepEqual(ch.calls, want) {
		t.Fatalf("a root group only: chown = %v, want %v", ch.calls, want)
	}

	// With files in it, or with an owner of its own, it is left alone.
	ch.calls = nil
	full := t.TempDir()
	if err := os.WriteFile(full+"/photo.jpg", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.statOwner = func(string) (uint32, uint32, error) { return 0, 0, nil }
	svc.adoptEmpty(ctx, d, full)
	svc.statOwner = func(string) (uint32, uint32, error) { return 994, 990, nil }
	svc.adoptEmpty(ctx, d, empty)
	if len(ch.calls) != 0 {
		t.Fatalf("a folder that was to be left alone: %v", ch.calls)
	}
}
