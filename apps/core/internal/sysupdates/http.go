package sysupdates

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the sysupdates API adapter.
type HTTP struct{ mod *Module }

// NewHTTP returns the HTTP adapter for mod.
func NewHTTP(mod *Module) *HTTP { return &HTTP{mod: mod} }

type (
	timelineIn struct {
		Q      string `query:"q" doc:"Case-insensitive substring filter over change names."`
		Source string `query:"source" enum:"manual,rollback"`
		Page   int    `query:"page" minimum:"1" default:"1"`
		Days   int    `query:"days" minimum:"1" maximum:"60" default:"7" doc:"Days per page."`
	}
	timelineOut struct{ Body UpdateTimeline }
	statusOut   struct{ Body SystemUpdateStatus }
	deployOut   struct{ Body CoreDeploy }
	deploysOut  struct{ Body []CoreDeploy }
	imageOut    struct{ Body ImageStatus }
	bootIn      struct {
		Body struct {
			Version string `json:"version" doc:"An installed image version; empty with mode default: the newest again."`
			Mode    string `json:"mode" enum:"once,default" doc:"once: at the next reboot only. default: every boot, until the next image is installed."`
			Reboot  bool   `json:"reboot,omitempty" doc:"Reboot after the answer."`
		}
	}
	autoIn struct {
		Body struct {
			At string `json:"at" doc:"A time of day such as 03:30, in the host's time zone; empty: only when asked."`
		}
	}
	installIn struct {
		// Optional: no body is a later reboot.
		Body *struct {
			Reboot string `json:"reboot,omitempty" doc:"Empty: the new version boots at the next reboot. now: reboot at once. HH:MM: reboot then, today."`
		} `required:"false"`
	}
)

// Register adds the sysupdates operations: the two GETs are read scope, the
// reboot is operator-only.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"sysupdates"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getSystemUpdatesTimeline", Method: http.MethodGet, Path: "/system-updates/timeline", Tags: tags,
		Summary: "System generations grouped by day.",
	}, h.getTimeline)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getSystemUpdateStatus", Method: http.MethodGet, Path: "/system-updates/status", Tags: tags,
		Summary: "The System card — release, newer release, reboot needed and the running generation.",
	}, h.getStatus)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getCoreDeploys", Method: http.MethodGet, Path: "/system-updates/deploys", Tags: tags,
		Summary: "Core deploys and rollbacks, newest first.",
	}, h.getDeploys)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deployCore", Method: http.MethodPost, Path: "/system-updates/deploy", Tags: tags,
		Summary:       "Deploy core from main.",
		Description:   "Fetches main, builds it, switches and checks the daemon and Caddy; if they do not come back the host returns to the generation before. The work outlives the daemon restart it causes: follow it in /system-updates/deploys.",
		DefaultStatus: http.StatusAccepted,
	}, h.deploy)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "rollbackCore", Method: http.MethodPost, Path: "/system-updates/rollback", Tags: tags,
		Summary: "Switch core back to the generation before.", DefaultStatus: http.StatusAccepted,
	}, h.rollback)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getImageStatus", Method: http.MethodGet, Path: "/system-updates/image", Tags: tags,
		Summary: "Image host: the booted, installed and staged versions of the image and the core extension.",
		Errors:  []int{http.StatusConflict},
	}, h.getImage)
	imageOp := func(id, path, summary, desc string) huma.Operation {
		return huma.Operation{OperationID: id, Method: http.MethodPost, Path: path, Tags: tags, Summary: summary, Description: desc,
			DefaultStatus: http.StatusAccepted, Errors: []int{http.StatusBadRequest, http.StatusConflict}}
	}
	httpx.Register(a, httpx.ScopeOperator, imageOp("buildImage", "/system-updates/image/build",
		"Image host: build the image and the core extension from main.",
		"Fetches main and runs image/build.sh; the result is staged in the update folder. Follow it in /system-updates/deploys."), h.build)
	httpx.Register(a, httpx.ScopeOperator, imageOp("downloadRelease", "/system-updates/image/download",
		"Image host: look for a newer release and download it.",
		"Asks the release server for its newest release and, when it is newer than what the host has, downloads it, checks its signed checksums and stages it in the update folder. Nothing is installed. Follow it in /system-updates/deploys."), h.download)
	httpx.Register(a, httpx.ScopeOperator, imageOp("updateCoreExtension", "/system-updates/image/core",
		"Image host: update the core extension, without a reboot.",
		"Merges the newest staged core extension and restarts the daemon and Caddy; if they do not come back, the version before goes back."), h.updateCore)
	httpx.Register(a, httpx.ScopeOperator, imageOp("installImage", "/system-updates/image/install",
		"Image host: install the newest staged image into the free slot.",
		"It boots at the next reboot, which this can start now or at a time. A version that does not boot falls back to the one before by itself."), h.install)
	httpx.Register(a, httpx.ScopeOperator, imageOp("installRelease", "/system-updates/image/release",
		"Image host: install what is staged, the image and the core extension, in one operation.",
		"The image goes into the free slot, then the core extension is merged and checked (the version before goes back when it does not start), then the reboot choice applies. Follow it in /system-updates/deploys."), h.installRelease)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "bootImage", Method: http.MethodPost, Path: "/system-updates/image/boot", Tags: tags,
		Summary:     "Image host: boot another installed image version, once or by default.",
		Description: "The way back to the version before, and the way to try a version again that failed its boots: one that comes up counts as good again. A default lasts until the next image is installed.",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict},
	}, h.boot)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "setImageAutoInstall", Method: http.MethodPut, Path: "/system-updates/image/auto-install", Tags: tags,
		Summary:     "Image host: the time of day a downloaded release installs by itself.",
		Description: "At that time a staged release is installed as installRelease does, with a reboot at once when it brings a new image. Each release is tried once.",
		Errors:      []int{http.StatusBadRequest, http.StatusConflict},
	}, h.autoInstall)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "rebootSystem", Method: http.MethodPost, Path: "/system-updates/reboot", Tags: tags,
		Summary: "Reboot the host.", Description: "The reboot starts a few seconds after the response.",
		DefaultStatus: http.StatusAccepted,
	}, h.reboot)
}

func (h *HTTP) getTimeline(ctx context.Context, in *timelineIn) (*timelineOut, error) {
	return &timelineOut{Body: h.mod.Timeline(ctx, Query{Q: in.Q, Source: in.Source, Page: in.Page, Days: in.Days})}, nil
}

func (h *HTTP) getStatus(ctx context.Context, _ *struct{}) (*statusOut, error) {
	return &statusOut{Body: h.mod.Status(ctx)}, nil
}

func (h *HTTP) getDeploys(ctx context.Context, _ *struct{}) (*deploysOut, error) {
	return &deploysOut{Body: h.mod.Deploys(ctx)}, nil
}

func (h *HTTP) deploy(ctx context.Context, _ *struct{}) (*deployOut, error) {
	return h.start(ctx, KindDeploy)
}

func (h *HTTP) rollback(ctx context.Context, _ *struct{}) (*deployOut, error) {
	return h.start(ctx, KindRollback)
}

func (h *HTTP) getImage(ctx context.Context, _ *struct{}) (*imageOut, error) {
	st, err := h.mod.ImageStatus(ctx)
	if err != nil {
		return nil, httpx.Conflict("not_image_host", "%s", err.Error())
	}
	return &imageOut{Body: st}, nil
}

func (h *HTTP) build(ctx context.Context, _ *struct{}) (*deployOut, error) {
	return startOut(h.mod.StartImage(ctx, KindBuild, ""))
}

func (h *HTTP) download(ctx context.Context, _ *struct{}) (*deployOut, error) {
	return startOut(h.mod.DownloadRelease(ctx))
}

func (h *HTTP) updateCore(ctx context.Context, _ *struct{}) (*deployOut, error) {
	return startOut(h.mod.StartImage(ctx, KindCoreUpdate, ""))
}

func (h *HTTP) install(ctx context.Context, in *installIn) (*deployOut, error) {
	reboot := ""
	if in.Body != nil {
		reboot = in.Body.Reboot
	}
	return startOut(h.mod.StartImage(ctx, KindImageUpdate, reboot))
}

func (h *HTTP) installRelease(ctx context.Context, in *installIn) (*deployOut, error) {
	reboot := ""
	if in.Body != nil {
		reboot = in.Body.Reboot
	}
	return startOut(h.mod.StartImage(ctx, KindInstall, reboot))
}

func imageStatusOut(st ImageStatus, err error) (*imageOut, error) {
	switch {
	case errors.Is(err, ErrNotImageHost):
		return nil, httpx.Conflict("not_image_host", "%s", err.Error())
	case errors.Is(err, ErrNoSuchVersion):
		return nil, httpx.NotFound("no_such_version", "%s", err.Error())
	case errors.Is(err, ErrBadBootMode):
		return nil, httpx.BadRequest("invalid_mode", "%s", err.Error())
	case errors.Is(err, ErrBadTime):
		return nil, httpx.BadRequest("invalid_time", "%s", err.Error())
	case err != nil:
		return nil, err
	}
	return &imageOut{Body: st}, nil
}

func (h *HTTP) boot(ctx context.Context, in *bootIn) (*imageOut, error) {
	return imageStatusOut(h.mod.BootImage(ctx, in.Body.Version, in.Body.Mode, in.Body.Reboot))
}

func (h *HTTP) autoInstall(ctx context.Context, in *autoIn) (*imageOut, error) {
	return imageStatusOut(h.mod.SetAutoInstall(ctx, in.Body.At))
}

func (h *HTTP) start(ctx context.Context, kind string) (*deployOut, error) {
	return startOut(h.mod.StartDeploy(ctx, kind))
}

func startOut(d CoreDeploy, err error) (*deployOut, error) {
	switch {
	case errors.Is(err, ErrBadReboot):
		return nil, httpx.BadRequest("invalid_reboot", "%s", err.Error())
	case errors.Is(err, ErrNotImageHost):
		return nil, httpx.Conflict("not_image_host", "%s", err.Error())
	case errors.Is(err, ErrImageHost):
		return nil, httpx.Conflict("image_host", "%s", err.Error())
	case errors.Is(err, ErrDeployRunning):
		return nil, httpx.Conflict("deploy_running", "%s", err.Error())
	case errors.Is(err, ErrDeployNotSet):
		return nil, httpx.Conflict("deploy_not_configured", "%s", err.Error())
	case errors.Is(err, ErrSecretsNotPushed):
		return nil, httpx.Conflict("secrets_not_pushed", "%s", err.Error())
	case err != nil:
		return nil, err
	}
	return &deployOut{Body: d}, nil
}

func (h *HTTP) reboot(context.Context, *struct{}) (*struct{}, error) {
	h.mod.Reboot()
	return nil, nil
}
