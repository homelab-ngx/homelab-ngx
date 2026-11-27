package stacks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store"
)

// A stack's service runs as its own user, and that user owns the host folder
// a path variable points at: ids are shared with the stack, not translated.
// The host keeps one id per name for that, the stack ids, and gives them to
// the folders the daemon makes. A folder that already has an owner keeps it,
// and the stack takes its ids from the folder as before.

// StackIDMin and StackIDMax bound the stack ids. Nothing on the host or in
// a stack hands out numbers there: system accounts sit below 1000, people
// count up from 1000, and Nix's build users start at 30001.
const (
	StackIDMin = 2000
	StackIDMax = 2099
)

// Places is where a folder for a stack can be made; the storage service
// satisfies it.
type Places interface {
	// FolderPlaces are the mounted pools and partitions, by directory.
	FolderPlaces(ctx context.Context) ([]string, error)
	// MakeFolder makes place/name (a subvolume on a pool) and returns its
	// path. An empty folder that is there already is used.
	MakeFolder(ctx context.Context, place, name string) (string, error)
}

// UsePlaces lets the dashboard make a path variable's folder.
func (s *Service) UsePlaces(p Places) { s.places = p }

// stackIDs is the table, by name.
func (s *Service) stackIDs(ctx context.Context) (map[string]uint32, error) {
	rows, err := s.st.Queries().ListStackIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]uint32, len(rows))
	for _, r := range rows {
		out[r.Name] = uint32(r.ID)
	}
	return out, nil
}

// StackIDs lists the host's stack ids, lowest first.
func (s *Service) StackIDs(ctx context.Context) (StackIDList, error) {
	rows, err := s.st.Queries().ListStackIDs(ctx)
	if err != nil {
		return StackIDList{}, err
	}
	out := StackIDList{Min: StackIDMin, Max: StackIDMax, IDs: []StackID{}}
	for _, r := range rows {
		out.IDs = append(out.IDs, StackID{Name: r.Name, ID: r.ID})
	}
	return out, nil
}

// idFor is the id of the user or group name: the one the host has given it,
// or the lowest free one, which it then keeps.
func (s *Service) idFor(ctx context.Context, name string) (uint32, error) {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	ids, err := s.stackIDs(ctx)
	if err != nil {
		return 0, err
	}
	if id, ok := ids[name]; ok {
		return id, nil
	}
	taken := map[uint32]bool{}
	for _, id := range ids {
		taken[id] = true
	}
	for id := uint32(StackIDMin); id <= StackIDMax; id++ {
		if taken[id] {
			continue
		}
		err := s.st.Queries().UpsertStackID(ctx, store.UpsertStackIDParams{Name: name, ID: int64(id), UpdatedAt: s.now().UnixMilli()})
		return id, err
	}
	return 0, httpx.Conflict("stack_ids_full", "all %d stack ids (%d to %d) are taken; see homelab stacks ids", StackIDMax-StackIDMin+1, StackIDMin, StackIDMax)
}

// SetStackID pins the id of a user or group name. Folders already made for
// the name keep the id they have.
func (s *Service) SetStackID(ctx context.Context, name string, id int64) (StackIDList, error) {
	if !accountRE.MatchString(name) {
		return StackIDList{}, httpx.Unprocessable("invalid_name", "%q is not a user or group name", name)
	}
	if id < StackIDMin || id > StackIDMax {
		return StackIDList{}, httpx.Unprocessable("invalid_id", "a stack id is %d to %d", StackIDMin, StackIDMax)
	}
	s.idMu.Lock()
	ids, err := s.stackIDs(ctx)
	if err == nil {
		for n, have := range ids {
			if n != name && int64(have) == id {
				err = httpx.Conflict("id_taken", "%d is %s's id", id, n)
			}
		}
	}
	if err == nil {
		err = s.st.Queries().UpsertStackID(ctx, store.UpsertStackIDParams{Name: name, ID: id, UpdatedAt: s.now().UnixMilli()})
	}
	s.idMu.Unlock()
	if err != nil {
		return StackIDList{}, err
	}
	return s.StackIDs(ctx)
}

// declaredPath is the stack's path variable varName, from the manifest it
// was fetched with, else the approved one.
func (s *Service) declaredPath(ctx context.Context, name, varName string) (Variable, error) {
	r, err := s.row(ctx, name)
	if err != nil {
		return Variable{}, err
	}
	for _, raw := range []string{r.FetchedManifest, r.ApprovedManifest} {
		if raw == "" {
			continue
		}
		m, err := ParseManifest([]byte(raw))
		if err != nil {
			continue
		}
		for _, d := range m.pathVars() {
			if d.Name == varName {
				return d, nil
			}
		}
	}
	return Variable{}, httpx.Unprocessable("not_a_path", "%s is not a path variable of %s", varName, name)
}

// ownerIDs are the stack ids of d's declared owner and group; -1 for one it
// does not declare.
func (s *Service) ownerIDs(ctx context.Context, d Variable) (uid, gid int, err error) {
	uid, gid = -1, -1
	if d.Owner != "" {
		id, err := s.idFor(ctx, d.Owner)
		if err != nil {
			return 0, 0, err
		}
		uid = int(id)
	}
	if d.Group != "" {
		id, err := s.idFor(ctx, d.Group)
		if err != nil {
			return 0, 0, err
		}
		gid = int(id)
	}
	return uid, gid, nil
}

var folderNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// CreateFolder makes the folder for the stack's path variable and stores it
// as the value: place/folder, a subvolume when the place is a pool, owned by
// the stack ids of the owner and group the manifest declares. An empty
// place is the host's only one; an empty folder name is the stack's name.
func (s *Service) CreateFolder(ctx context.Context, name, varName string, in StackFolderCreate) (Stack, error) {
	d, err := s.declaredPath(ctx, name, varName)
	if err != nil {
		return Stack{}, err
	}
	if s.places == nil {
		return Stack{}, httpx.Conflict("no_places", "this host has nowhere to make a folder")
	}
	place := in.Place
	if place == "" {
		places, err := s.places.FolderPlaces(ctx)
		if err != nil {
			return Stack{}, err
		}
		switch len(places) {
		case 0:
			return Stack{}, httpx.Conflict("no_places", "no pool or partition is mounted: mount one on the Storage page first")
		case 1:
			place = places[0]
		default:
			return Stack{}, httpx.Unprocessable("place_needed", "say where: %s", strings.Join(places, ", "))
		}
	}
	folder := in.Folder
	if folder == "" {
		folder = name
	}
	if !folderNameRE.MatchString(folder) {
		return Stack{}, httpx.Unprocessable("invalid_folder", "%q is not a folder name: letters, digits, '.', '_' and '-'", folder)
	}
	uid, gid, err := s.ownerIDs(ctx, d)
	if err != nil {
		return Stack{}, err
	}
	path, err := s.places.MakeFolder(ctx, place, folder)
	if err != nil {
		return Stack{}, httpx.Unprocessable("folder_not_made", "%v", err)
	}
	if uid >= 0 || gid >= 0 {
		if err := s.chown(ctx, path, uid, gid, false); err != nil {
			return Stack{}, fmt.Errorf("setting the owner of %s: %w", path, err)
		}
	}
	s.log.Info("stack folder made", "stack", name, "variable", varName, "path", path, "uid", uid, "gid", gid)
	return s.SetVariable(ctx, name, varName, StackVariableSet{Value: path})
}

// adoptEmpty gives a root-owned, empty folder the stack ids of d's owner
// and group: a folder just made by hand, which the stack could not use. One
// with files in it is left alone and flagged.
func (s *Service) adoptEmpty(ctx context.Context, d Variable, path string) {
	if d.Owner == "" && d.Group == "" {
		return
	}
	uid, gid, err := s.statOwner(path)
	if err != nil || !((d.Owner != "" && uid == 0) || (d.Group != "" && gid == 0)) {
		return
	}
	if entries, err := os.ReadDir(path); err != nil || len(entries) > 0 {
		return
	}
	wantUID, wantGID, err := s.ownerIDs(ctx, d)
	if err != nil {
		return
	}
	if uid != 0 {
		wantUID = -1
	}
	if gid != 0 {
		wantGID = -1
	}
	if err := s.chown(ctx, path, wantUID, wantGID, false); err != nil {
		s.log.Warn("empty folder not given the stack ids", "path", path, "error", err)
	}
}

// OwnFolder changes the owner of the stack's path folder, and of everything
// in it, to the stack ids of the owner and group the manifest declares. It
// cannot be undone, so confirm must be the folder's path, and the stack must
// be stopped: its services would lose their files under them.
func (s *Service) OwnFolder(ctx context.Context, name, varName string, in StackFolderOwn) (Stack, error) {
	d, err := s.declaredPath(ctx, name, varName)
	if err != nil {
		return Stack{}, err
	}
	if d.Owner == "" && d.Group == "" {
		return Stack{}, httpx.Unprocessable("no_owner", "%s declares no owner or group for its folder", varName)
	}
	s.varsMu.Lock()
	vars, err := s.loadVars(name)
	s.varsMu.Unlock()
	if err != nil {
		return Stack{}, varsUnreadable(name, err)
	}
	path, src := resolve(d, vars)
	if src == SourceUnset {
		return Stack{}, httpx.Conflict("unset", "%s has no folder", varName)
	}
	if in.Confirm != path {
		return Stack{}, httpx.Unprocessable("confirm", "type %s to change its owner", path)
	}
	if err := validHostPath(path); err != nil {
		return Stack{}, httpx.Unprocessable("invalid_path", "%v", err)
	}
	cur, err := s.Get(ctx, name)
	if err != nil {
		return Stack{}, err
	}
	if cur.Running {
		return Stack{}, httpx.Conflict("stack_running", "stop %s first: its services would lose their files while the owner changes", name)
	}
	oldUID, oldGID, err := s.statOwner(path)
	if err != nil {
		return Stack{}, httpx.Unprocessable("invalid_path", "reading the owner of %s: %v", path, err)
	}
	uid, gid, err := s.ownerIDs(ctx, d)
	if err != nil {
		return Stack{}, err
	}
	s.log.Warn("changing a stack folder's owner", "stack", name, "variable", varName, "path", path,
		"from_uid", oldUID, "from_gid", oldGID, "to_uid", uid, "to_gid", gid)
	if err := s.chown(ctx, path, uid, gid, true); err != nil {
		return Stack{}, fmt.Errorf("changing the owner of %s: %w", path, err)
	}
	s.refreshHostIDs(ctx, name)
	return s.Get(ctx, name)
}

// chownPath is the default chown: uid or gid -1 leaves that one as it is.
// Recursive, it does not follow links.
func chownPath(r cmdrunner.Runner) func(ctx context.Context, path string, uid, gid int, recursive bool) error {
	return func(ctx context.Context, path string, uid, gid int, recursive bool) error {
		if !recursive {
			return os.Lchown(path, uid, gid)
		}
		spec := ""
		if uid >= 0 {
			spec = strconv.Itoa(uid)
		}
		if gid >= 0 {
			spec += ":" + strconv.Itoa(gid)
		}
		if spec == "" {
			return errors.New("no owner to set")
		}
		_, err := r.Run(ctx, cmdrunner.Cmd{Module: "stacks", Name: "chown", Args: []string{"-hR", "--", spec, path}})
		return err
	}
}

// OwnerState values.
const (
	OwnerOK           = "ok"            // the folder has the stack ids
	OwnerOwn          = "own"           // it has ids of its own, which the stack takes
	OwnerRoot         = "root"          // root owns it: the stack cannot use it
	OwnerGroupDiffers = "group_differs" // another stack's folder has another id for the same group
)

// ownerView fills in, for each path variable with a declared owner or
// group and a folder, who owns the folder, the stack ids of the names, and
// how the two stand.
func (s *Service) ownerView(ctx context.Context, stack string, vars []StackVariable) {
	var ids map[string]uint32
	var others map[string][]uint32
	for i := range vars {
		v := &vars[i]
		if v.Kind != StackVariableKindPath || (v.Owner == "" && v.Group == "") {
			continue
		}
		path := v.Value
		if path == "" && v.Default != nil {
			path = *v.Default
		}
		if path == "" {
			continue
		}
		uid, gid, err := s.statOwner(path)
		if err != nil {
			continue
		}
		if ids == nil {
			if ids, err = s.stackIDs(ctx); err != nil {
				return
			}
			others = s.otherGroupIDs(ctx, stack)
		}
		fu, fg := int64(uid), int64(gid)
		v.FolderUID, v.FolderGID = &fu, &fg
		state := OwnerOK
		if v.Owner != "" {
			if id, ok := ids[v.Owner]; ok {
				su := int64(id)
				v.StackUID = &su
			}
			if v.StackUID == nil || *v.StackUID != fu {
				state = OwnerOwn
			}
		}
		if v.Group != "" {
			if id, ok := ids[v.Group]; ok {
				sg := int64(id)
				v.StackGID = &sg
			}
			if v.StackGID == nil || *v.StackGID != fg {
				state = OwnerOwn
			}
			for _, other := range others[v.Group] {
				if other != gid {
					state = OwnerGroupDiffers
				}
			}
		}
		if (v.Owner != "" && uid == 0) || (v.Group != "" && gid == 0) {
			state = OwnerRoot
		}
		v.OwnerState = state
	}
}

var hostGroupRE = regexp.MustCompile(`group ([^=, ]+)=(\d+)`)

// otherGroupIDs are the ids the other stacks' folders have for each group
// name, from the ids their rows record.
func (s *Service) otherGroupIDs(ctx context.Context, stack string) map[string][]uint32 {
	out := map[string][]uint32{}
	rows, err := s.st.Queries().ListStacks(ctx)
	if err != nil {
		return out
	}
	for _, r := range rows {
		if r.Name == stack {
			continue
		}
		for _, m := range hostGroupRE.FindAllStringSubmatch(r.HostIds, -1) {
			if id, err := strconv.ParseUint(m[2], 10, 32); err == nil {
				out[m[1]] = append(out[m[1]], uint32(id))
			}
		}
	}
	for _, ids := range out {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	return out
}
