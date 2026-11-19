package storage

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the storage API adapter.
type HTTP struct{ svc *Service }

// NewHTTP returns the HTTP adapter for svc.
func NewHTTP(svc *Service) *HTTP { return &HTTP{svc: svc} }

// problem maps domain errors: 409 subvolume_not_empty, 404 unknown_pool,
// 403 outside_roots, 404 not_found, 400 invalid_name, 409 name_taken. Command failures carry their
// message to the client as storage_failed (the operator needs mount's stderr).
func problem(err error) error {
	switch {
	case errors.Is(err, ErrSubvolumeNotEmpty):
		return httpx.Conflict("subvolume_not_empty", "%s", err)
	case errors.Is(err, ErrUnknownPool):
		return httpx.NotFound("unknown_pool", "%s", err)
	case errors.Is(err, ErrOutsideRoots):
		return httpx.Forbidden("outside_roots", "%s", err)
	case errors.Is(err, ErrNoSuchFolder):
		return httpx.NotFound("not_found", "%s", err)
	case errors.Is(err, ErrMountName):
		return httpx.BadRequest("invalid_name", "%s", err)
	case errors.Is(err, ErrNameTaken):
		return httpx.Conflict("name_taken", "%s", err)
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
	usageIn        struct{ Body SubvolumeUsageRequest }
	usageOut       struct{ Body SubvolumeUsage }
	subvolumeIn    struct{ Body SubvolumeRequest }
	delSubvolumeIn struct {
		Path string `query:"path" required:"true" minLength:"1"`
	}
	browseIn struct {
		Path string `query:"path" default:"/" doc:"Absolute host path of the folder."`
	}
	browseOut      struct{ Body Listing }
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
		OperationID: "getSubvolumeUsage", Method: http.MethodPost, Path: "/storage/subvolume-usage", Tags: tags,
		Summary: "Disk usage of subvolumes (slow; computed on demand).",
	}, h.getSubvolumeUsage)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "createSubvolume", Method: http.MethodPost, Path: "/storage/subvolume", Tags: tags,
		Summary: "Create a subvolume.", DefaultStatus: http.StatusCreated,
	}, h.createSubvolume)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteSubvolume", Method: http.MethodDelete, Path: "/storage/subvolume", Tags: tags,
		Summary:       "Delete an empty subvolume.",
		Description:   "`409 subvolume_not_empty` when the subvolume still holds data.",
		DefaultStatus: http.StatusNoContent,
	}, h.deleteSubvolume)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "browseFolder", Method: http.MethodGet, Path: "/storage/browse", Tags: tags,
		Summary: "List a folder in a subvolume or a stack's host folder.",
		Description: "Read-only. Above those folders only the folders leading to one are listed. " +
			"`403 outside_roots` for any other path, `404 not_found` for a missing folder. At most 5,000 entries.",
	}, h.browse)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getBrowseUsage", Method: http.MethodGet, Path: "/storage/browse/usage", Tags: tags,
		Summary: "bcdu's figures for the folders in a browsed folder (bcachefs only).",
		Description: "One bcdu run measures a whole subvolume or stack folder and is kept for 30 minutes, " +
			"so the first call takes a few seconds.",
	}, h.browseUsage)
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

func (h *HTTP) deleteSubvolume(ctx context.Context, in *delSubvolumeIn) (*struct{}, error) {
	if err := h.svc.DeleteSubvolume(ctx, in.Path); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}
