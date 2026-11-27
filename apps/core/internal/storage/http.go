package storage

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/storage/bcachefs"
)

// HTTP is the storage API adapter.
type HTTP struct{ svc *Service }

// NewHTTP returns the HTTP adapter for svc.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

// problem maps domain errors: 409 subvolume_not_empty, 404 unknown_pool,
// 403 outside_roots, 404 not_found, 400 invalid_name, 409 name_taken, and
// the file editor's not_a_file, symlink, changed_on_disk, exists, is_root. Command failures carry their
// message to the client as storage_failed (the operator needs mount's stderr).
func problem(err error) error {
	switch {
	case errors.Is(err, ErrSubvolumeNotEmpty):
		return httpx.Conflict("subvolume_not_empty", "%s", err)
	case errors.Is(err, ErrUnknownPool):
		return httpx.NotFound("unknown_pool", "%s", err)
	case errors.Is(err, ErrUnknownPartition):
		return httpx.NotFound("unknown_partition", "%s", err)
	case errors.Is(err, ErrPartitionState):
		return httpx.Conflict("partition_state", "%s", err)
	case errors.Is(err, ErrOutsideRoots):
		return httpx.Forbidden("outside_roots", "%s", err)
	case errors.Is(err, ErrNoSuchFolder):
		return httpx.NotFound("not_found", "%s", err)
	case errors.Is(err, ErrMountName):
		return httpx.BadRequest("invalid_name", "%s", err)
	case errors.Is(err, ErrNameTaken):
		return httpx.Conflict("name_taken", "%s", err)
	case errors.Is(err, ErrBusy):
		return httpx.Conflict("busy", "%s", err)
	case errors.Is(err, ErrPoolInUse):
		return httpx.Conflict("pool_in_use", "%s", err)
	case errors.Is(err, ErrDiskNotEligible), errors.Is(err, bcachefs.ErrDeviceBusy):
		return httpx.Conflict("disk_not_eligible", "%s", err)
	case errors.Is(err, ErrUnknownDisk):
		return httpx.NotFound("unknown_disk", "%s", err)
	case errors.Is(err, ErrPoolState):
		return httpx.Conflict("pool_state", "%s", err)
	case errors.Is(err, ErrTooFewDisks):
		return httpx.Conflict("too_few_disks", "%s", err)
	case errors.Is(err, ErrLastOfRole):
		return httpx.Conflict("last_of_role", "%s", err)
	case errors.Is(err, ErrNoRoom):
		return httpx.Conflict("not_enough_room", "%s", err)
	case errors.Is(err, ErrPoolDegraded):
		return httpx.Conflict("pool_degraded", "%s", err)
	case errors.Is(err, ErrImageTooOld):
		return httpx.Conflict("image_too_old", "%s", err)
	case errors.Is(err, ErrConfirm):
		return httpx.Unprocessable("confirm", "%s", err)
	case errors.Is(err, ErrNoSuchFile):
		return httpx.NotFound("not_found", "%s", err)
	case errors.Is(err, ErrNotAFile):
		return httpx.Unprocessable("not_a_file", "%s", err)
	case errors.Is(err, ErrSymlink):
		return httpx.Unprocessable("symlink", "%s", err)
	case errors.Is(err, ErrTooLarge):
		return httpx.Unprocessable("too_large", "%s", err)
	case errors.Is(err, ErrChangedOnDisk):
		return httpx.Conflict("changed_on_disk", "%s", err)
	case errors.Is(err, ErrExists):
		return httpx.Conflict("exists", "%s", err)
	case errors.Is(err, ErrIsRoot):
		return httpx.Forbidden("is_root", "%s", err)
	case errors.Is(err, ErrHoldsRoot):
		return httpx.Forbidden("holds_root", "%s", err)
	case errors.Is(err, ErrMountPoint):
		return httpx.Conflict("mount_point", "%s", err)
	case errors.Is(err, ErrUnknownSubvolume):
		return httpx.NotFound("unknown_subvolume", "%s", err)
	case errors.Is(err, ErrNotTiered):
		return httpx.Conflict("pool_not_tiered", "%s", err)
	case errors.Is(err, ErrInvalidProfile):
		return httpx.BadRequest("invalid_profile", "%s", err)
	}
	return httpx.Internal("storage_failed", "%s", err)
}

type (
	statusOut    struct{ Body StorageStatus }
	mountIn      struct{ Body MountRequest }
	unmountIn    struct{ Body UnmountRequest }
	updatePoolIn struct {
		Uuid string `path:"uuid"`
		Body PoolPatch
	}
	createPoolIn struct{ Body PoolCreateRequest }
	poolOut      struct{ Body Pool }
	deletePoolIn struct {
		Uuid    string `path:"uuid"`
		Confirm string `query:"confirm" doc:"The pool's name. Every disk in the pool is erased."`
	}
	addDiskIn struct {
		Uuid string `path:"uuid"`
		Body PoolDiskRequest
	}
	removeDiskIn struct {
		Uuid string `path:"uuid"`
		Disk string `query:"disk" required:"true" minLength:"1" doc:"The member's device, such as /dev/sdc."`
	}
	partitionIn struct {
		Uuid string `path:"uuid"`
	}
	mountPartitionIn struct {
		Uuid string `path:"uuid"`
		Body PartitionMountRequest
	}
	usageIn           struct{ Body SubvolumeUsageRequest }
	usageOut          struct{ Body SubvolumeUsage }
	subvolumeIn       struct{ Body SubvolumeRequest }
	updateSubvolumeIn struct{ Body SubvolumeProfileRequest }
	delSubvolumeIn    struct {
		Path string `query:"path" required:"true" minLength:"1"`
	}
	browseIn struct {
		Path string `query:"path" default:"/" doc:"Absolute host path of the folder."`
	}
	browseOut struct{ Body Listing }
	fileIn    struct {
		Path string `query:"path" required:"true" minLength:"2" doc:"Absolute host path of the file."`
	}
	fileOut       struct{ Body HostFile }
	writeFileIn   struct{ Body FileWrite }
	deleteEntryIn struct {
		Path    string `query:"path" required:"true" minLength:"2" doc:"Absolute host path of the file or folder."`
		Confirm string `query:"confirm" doc:"The folder's name. Needed for a folder that holds anything."`
	}
	browseUsageOut struct{ Body FolderFigures }
)

// Register adds the storage operations. Reading the status is read scope;
// everything else changes the pools and is operator-only.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"storage"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getStorage", Method: http.MethodGet, Path: "/storage", Tags: tags,
		Summary: "Pools, unassigned disks and subvolumes (cached for ~10 s).",
	}, h.getStorage)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "mountPool", Method: http.MethodPost, Path: "/storage/mount", Tags: tags,
		Summary: "Mount a bcachefs pool and remember it for auto-mount.", DefaultStatus: http.StatusNoContent,
	}, h.mountPool)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "unmountPool", Method: http.MethodPost, Path: "/storage/unmount", Tags: tags,
		Summary: "Unmount a pool.", DefaultStatus: http.StatusNoContent,
	}, h.unmountPool)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "updatePool", Method: http.MethodPatch, Path: "/storage/pools/{uuid}", Tags: tags,
		Summary: "Change whether a pool is mounted automatically at boot.", DefaultStatus: http.StatusNoContent,
	}, h.updatePool)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "createPool", Method: http.MethodPost, Path: "/storage/pools", Tags: tags,
		Summary: "Make a pool from spare disks and mount it.",
		Description: "The disks come from the status's `unassigned` list. With SSDs and HDDs together the SSDs take new " +
			"writes and the data moves to the HDDs. `422 confirm` when a disk has contents and `confirm` is not the name, " +
			"`409 disk_not_eligible`, `409 too_few_disks`, `409 name_taken`.",
		DefaultStatus: http.StatusCreated,
	}, h.createPool)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deletePool", Method: http.MethodDelete, Path: "/storage/pools/{uuid}", Tags: tags,
		Summary: "Unmount a pool, erase its disks and forget it.",
		Description: "`409 pool_in_use` while a stack or a backup keeps a folder on it, `422 confirm` without its name, " +
			"`409 busy` while a disk is being removed.",
		DefaultStatus: http.StatusNoContent,
	}, h.deletePool)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "addPoolDisk", Method: http.MethodPost, Path: "/storage/pools/{uuid}/disks", Tags: tags,
		Summary: "Add a spare disk to a mounted pool.",
		Description: "`422 confirm` when the disk has contents and `confirm` is not the pool's name, " +
			"`409 pool_state` when the pool is not mounted, `409 pool_degraded`, `409 busy`.",
		DefaultStatus: http.StatusNoContent,
	}, h.addPoolDisk)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "removePoolDisk", Method: http.MethodDelete, Path: "/storage/pools/{uuid}/disks", Tags: tags,
		Summary: "Start removing a disk from a mounted pool.",
		Description: "The disk's data moves to the others first, which can take hours; then the disk is erased. " +
			"The pool's `operation` in the storage status shows how far it is. `409 too_few_disks`, " +
			"`409 last_of_role`, `409 not_enough_room`, `409 pool_degraded`, `409 busy`.",
		DefaultStatus: http.StatusAccepted,
	}, h.removePoolDisk)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "cancelPoolOperation", Method: http.MethodDelete, Path: "/storage/pools/{uuid}/operation", Tags: tags,
		Summary:       "Stop the removal of a disk, or clear one that ended.",
		Description:   "The disk goes back to the pool; data already moved stays on the other disks.",
		DefaultStatus: http.StatusNoContent,
	}, h.cancelPoolOperation)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "mountPartition", Method: http.MethodPost, Path: "/storage/partitions/{uuid}/mount", Tags: tags,
		Summary:       "Mount a partition under /var/mnt and remember it for the next boot.",
		Description:   "`409 name_taken` when a pool or another partition has the name, `409 partition_state` when it is mounted.",
		DefaultStatus: http.StatusNoContent,
	}, h.mountPartition)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "unmountPartition", Method: http.MethodPost, Path: "/storage/partitions/{uuid}/unmount", Tags: tags,
		Summary: "Unmount a partition and stop mounting it at boot.", DefaultStatus: http.StatusNoContent,
	}, h.unmountPartition)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "updatePartition", Method: http.MethodPatch, Path: "/storage/partitions/{uuid}", Tags: tags,
		Summary: "Change whether a partition is mounted automatically at boot.", DefaultStatus: http.StatusNoContent,
	}, h.updatePartition)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getSubvolumeUsage", Method: http.MethodPost, Path: "/storage/subvolume-usage", Tags: tags,
		Summary: "Disk usage of subvolumes (slow; computed on demand).",
	}, h.getSubvolumeUsage)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "createSubvolume", Method: http.MethodPost, Path: "/storage/subvolume", Tags: tags,
		Summary: "Create a subvolume.", DefaultStatus: http.StatusCreated,
	}, h.createSubvolume)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "updateSubvolume", Method: http.MethodPatch, Path: "/storage/subvolume", Tags: tags,
		Summary: "Change placement profile or copy count on a subvolume.",
		Description: "`404 unknown_subvolume` when the path is not a subvolume on a mounted pool, " +
			"`409 pool_not_tiered` when setting a placement profile on an untiered pool, " +
			"`409 too_few_disks` when copies exceeds the pool's disk count, `409 image_too_old` on older tools.",
		DefaultStatus: http.StatusNoContent,
	}, h.updateSubvolume)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteSubvolume", Method: http.MethodDelete, Path: "/storage/subvolume", Tags: tags,
		Summary:       "Delete an empty subvolume.",
		Description:   "`409 subvolume_not_empty` when the subvolume still holds data.",
		DefaultStatus: http.StatusNoContent,
	}, h.deleteSubvolume)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "browseFolder", Method: http.MethodGet, Path: "/storage/browse", Tags: tags,
		Summary: "List a folder in a subvolume or a stack's host folder.",
		Description: "Above those folders only the folders leading to one are listed. " +
			"`403 outside_roots` for any other path, `404 not_found` for a missing folder. At most 5,000 entries.",
	}, h.browse)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getBrowseUsage", Method: http.MethodGet, Path: "/storage/browse/usage", Tags: tags,
		Summary: "bcdu's figures for the folders in a browsed folder (bcachefs only).",
		Description: "One bcdu run measures a whole subvolume or stack folder and is kept for 30 minutes, " +
			"so the first call takes a few seconds.",
	}, h.browseUsage)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "readFile", Method: http.MethodGet, Path: "/storage/file", Tags: tags,
		Summary: "Read a file in a subvolume or a stack's host folder.",
		Description: "A file over 1 MiB, or one that is not UTF-8, comes back as binary without its content. " +
			"`403 outside_roots`, `404 not_found`, `422 not_a_file`.",
	}, h.readFile)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "writeFile", Method: http.MethodPut, Path: "/storage/file", Tags: tags,
		Summary: "Save a file in a subvolume or a stack's host folder.",
		Description: "Written to a temporary file and renamed over the old one. An existing file keeps its mode and owner; " +
			"a new one takes its folder's owner and mode 0644. `409 changed_on_disk` when version is stale, " +
			"`409 exists` for create on a taken path, `422 symlink` or `not_a_file` for anything but a file.",
	}, h.writeFile)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteEntry", Method: http.MethodDelete, Path: "/storage/entry", Tags: tags,
		Summary: "Delete a file, symlink or folder in a subvolume or a stack's host folder.",
		Description: "A folder goes with everything in it, and needs confirm to be its name when it holds anything (`422 confirm`). " +
			"A symlink is deleted, not what it points to. `403 is_root` or `holds_root` for a subvolume, mounted partition " +
			"or stack folder, `409 mount_point` when something is mounted inside.",
		DefaultStatus: http.StatusNoContent,
	}, h.deleteEntry)
}

func (h *HTTP) readFile(ctx context.Context, in *fileIn) (*fileOut, error) {
	f, err := h.svc.ReadFile(ctx, in.Path)
	if err != nil {
		return nil, problem(err)
	}
	return &fileOut{Body: f}, nil
}

func (h *HTTP) writeFile(ctx context.Context, in *writeFileIn) (*fileOut, error) {
	f, err := h.svc.WriteFile(ctx, in.Body)
	if err != nil {
		return nil, problem(err)
	}
	return &fileOut{Body: f}, nil
}

func (h *HTTP) deleteEntry(ctx context.Context, in *deleteEntryIn) (*struct{}, error) {
	if err := h.svc.DeleteEntry(ctx, in.Path, in.Confirm); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) browse(ctx context.Context, in *browseIn) (*browseOut, error) {
	l, err := h.svc.Browse(ctx, in.Path)
	if err != nil {
		return nil, problem(err)
	}
	return &browseOut{Body: l}, nil
}

func (h *HTTP) browseUsage(ctx context.Context, in *browseIn) (*browseUsageOut, error) {
	f, err := h.svc.BrowseUsage(ctx, in.Path)
	if err != nil {
		return nil, problem(err)
	}
	return &browseUsageOut{Body: f}, nil
}

func (h *HTTP) getStorage(ctx context.Context, _ *struct{}) (*statusOut, error) {
	st, err := h.svc.Status(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &statusOut{Body: st}, nil
}

func (h *HTTP) mountPool(ctx context.Context, in *mountIn) (*struct{}, error) {
	if err := h.svc.Mount(ctx, in.Body.Devices, in.Body.Name); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) unmountPool(ctx context.Context, in *unmountIn) (*struct{}, error) {
	if err := h.svc.Unmount(ctx, in.Body.Mountpoint); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) updatePool(ctx context.Context, in *updatePoolIn) (*struct{}, error) {
	if err := h.svc.SetAutoMount(ctx, in.Uuid, in.Body.AutoMount); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) getSubvolumeUsage(ctx context.Context, in *usageIn) (*usageOut, error) {
	return &usageOut{Body: SubvolumeUsage{Usage: h.svc.SubvolumeUsage(ctx, in.Body.Paths)}}, nil
}

func (h *HTTP) createSubvolume(ctx context.Context, in *subvolumeIn) (*struct{}, error) {
	if err := h.svc.CreateSubvolume(ctx, in.Body.Path); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) updateSubvolume(ctx context.Context, in *updateSubvolumeIn) (*struct{}, error) {
	if err := h.svc.UpdateSubvolume(ctx, in.Body); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) deleteSubvolume(ctx context.Context, in *delSubvolumeIn) (*struct{}, error) {
	if err := h.svc.DeleteSubvolume(ctx, in.Path); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) mountPartition(ctx context.Context, in *mountPartitionIn) (*struct{}, error) {
	if err := h.svc.MountPartition(ctx, in.Uuid, in.Body.Name, in.Body.ReadOnly); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) unmountPartition(ctx context.Context, in *partitionIn) (*struct{}, error) {
	if err := h.svc.UnmountPartition(ctx, in.Uuid); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) updatePartition(ctx context.Context, in *updatePoolIn) (*struct{}, error) {
	if err := h.svc.SetPartitionAutoMount(ctx, in.Uuid, in.Body.AutoMount); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) createPool(ctx context.Context, in *createPoolIn) (*poolOut, error) {
	p, err := h.svc.CreatePool(ctx, in.Body.Name, in.Body.Disks, in.Body.Copies, in.Body.Confirm)
	if err != nil {
		return nil, problem(err)
	}
	return &poolOut{Body: p}, nil
}

func (h *HTTP) deletePool(ctx context.Context, in *deletePoolIn) (*struct{}, error) {
	if err := h.svc.DeletePool(ctx, in.Uuid, in.Confirm); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) addPoolDisk(ctx context.Context, in *addDiskIn) (*struct{}, error) {
	if err := h.svc.AddDisk(ctx, in.Uuid, in.Body.Disk, in.Body.Confirm); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}

func (h *HTTP) removePoolDisk(ctx context.Context, in *removeDiskIn) (*poolOut, error) {
	p, err := h.svc.RemoveDisk(ctx, in.Uuid, in.Disk)
	if err != nil {
		return nil, problem(err)
	}
	return &poolOut{Body: p}, nil
}

func (h *HTTP) cancelPoolOperation(_ context.Context, in *deletePoolIn) (*struct{}, error) {
	if err := h.svc.CancelRemoval(in.Uuid); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}
