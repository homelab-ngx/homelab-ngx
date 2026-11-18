package installer

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/hostcfg"
	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the installer slice of the API. Every host has it; only the
// installer stick's says active.
type HTTP struct{ s *Service }

// NewHTTP returns the HTTP adapter for s (nil on any host but the stick).
func NewHTTP(s *Service) *HTTP { return &HTTP{s: s} }

type stateOut struct{ Body InstallerState }
type claimOut struct {
	Body struct {
		Token string `json:"token" doc:"Send it as X-Installer-Token with every change."`
	}
}
type tokenIn struct {
	Token string `header:"X-Installer-Token" doc:"From claimInstaller."`
}
type planIn struct {
	Body struct {
		Disk   string `json:"disk" doc:"Kernel name (nvme0n1)."`
		Erase  bool   `json:"erase" doc:"Erase the disk; else install into its free space."`
		ESP    string `json:"esp,omitempty" required:"false" doc:"Another disk's EFI partition (nvme1n1p3) for the boot loader; empty for one on this disk."`
		Region uint64 `json:"region,omitempty" required:"false" doc:"Into free space: the byte offset where the free region to fill starts; left out, the first that fits."`
	}
}
type planOut struct{ Body InstallPlan }
type deleteIn struct {
	Token string `header:"X-Installer-Token"`
	Disk  string `path:"disk"`
	Part  string `path:"part"`
	Body  struct {
		Confirm string `json:"confirm" doc:"The partition's name, typed by the user."`
	}
}
type installIn struct {
	Token string `header:"X-Installer-Token"`
	Body  struct {
		Disk     string       `json:"disk"`
		Erase    bool         `json:"erase"`
		ESP      string       `json:"esp,omitempty" required:"false" doc:"Another disk's EFI partition for the boot loader; empty for one on this disk."`
		Region   uint64       `json:"region,omitempty" required:"false" doc:"Into free space: the byte offset where the free region to fill starts; left out, the first that fits."`
		Hostname string       `json:"hostname" doc:"The installed host's name: it answers at <hostname>.local."`
		Timezone string       `json:"timezone,omitempty" doc:"An IANA zone such as Europe/Stockholm; empty keeps UTC."`
		SSHKeys  []string     `json:"ssh_keys,omitempty" required:"false" doc:"Root's authorized SSH public keys on the installed system."`
		IPv4     hostcfg.IPv4 `json:"ipv4,omitempty" required:"false" doc:"DHCP (when left out), or a static address for the installed system."`
	}
}

// Register adds the operations, all on the operator API.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"installer"}
	errs := []int{http.StatusBadRequest, http.StatusConflict}
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getInstaller", Method: http.MethodGet, Path: "/installer", Tags: tags,
		Summary: "The installer: whether it runs, the disks and the install's progress.",
	}, h.state)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "claimInstaller", Method: http.MethodPost, Path: "/installer/claim", Tags: tags, Errors: errs,
		Summary: "Take the installer: the first browser to ask gets it, others see it in use.",
	}, h.claim)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "planInstall", Method: http.MethodPost, Path: "/installer/plan", Tags: tags, Errors: errs,
		Summary: "What an install would do to a disk, as systemd-repart plans it. Changes nothing.",
	}, h.plan)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteInstallerPartition", Method: http.MethodDelete, Path: "/installer/disks/{disk}/partitions/{part}",
		Tags: tags, Errors: errs, Summary: "Delete a partition to make room. The body repeats its name.",
	}, h.deletePart)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "install", Method: http.MethodPost, Path: "/installer/install", Tags: tags, Errors: errs,
		Summary: "Install onto a disk; follow it with getInstaller.",
	}, h.install)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "powerOffInstaller", Method: http.MethodPost, Path: "/installer/poweroff", Tags: tags, Errors: errs,
		Summary: "Turn the machine off after the install, so the stick can come out.",
	}, h.powerOff)
}

func problem(err error) error {
	switch {
	case errors.Is(err, ErrClaimed):
		return httpx.Conflict("installer_claimed", "%s", err)
	case errors.Is(err, ErrBusy):
		return httpx.Conflict("install_busy", "%s", err)
	case errors.Is(err, ErrInvalid):
		return httpx.BadRequest("invalid_install", "%s", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	}
	return err
}

func (h *HTTP) active() error {
	if h.s == nil {
		return httpx.Conflict("not_installer", "this host is not running the installer")
	}
	return nil
}

func (h *HTTP) state(ctx context.Context, _ *struct{}) (*stateOut, error) {
	st, err := h.s.State(ctx)
	if err != nil {
		return nil, err
	}
	return &stateOut{Body: st}, nil
}

func (h *HTTP) claim(_ context.Context, _ *struct{}) (*claimOut, error) {
	if err := h.active(); err != nil {
		return nil, err
	}
	t, err := h.s.Claim()
	if err != nil {
		return nil, problem(err)
	}
	out := &claimOut{}
	out.Body.Token = t
	return out, nil
}

func (h *HTTP) plan(ctx context.Context, in *planIn) (*planOut, error) {
	if err := h.active(); err != nil {
		return nil, err
	}
	p, err := h.s.Plan(ctx, in.Body.Disk, in.Body.Erase, in.Body.ESP, in.Body.Region)
	if err != nil {
		return nil, problem(err)
	}
	return &planOut{Body: p}, nil
}

func (h *HTTP) deletePart(ctx context.Context, in *deleteIn) (*stateOut, error) {
	if err := h.active(); err != nil {
		return nil, err
	}
	if err := h.s.DeletePartition(ctx, in.Token, in.Disk, in.Part, in.Body.Confirm); err != nil {
		return nil, problem(err)
	}
	return h.state(ctx, nil)
}

func (h *HTTP) install(ctx context.Context, in *installIn) (*stateOut, error) {
	if err := h.active(); err != nil {
		return nil, err
	}
	if err := h.s.Install(ctx, in.Token, in.Body.Disk, in.Body.Erase, Setup{ESP: in.Body.ESP, Region: in.Body.Region, Hostname: in.Body.Hostname, Timezone: in.Body.Timezone, SSHKeys: in.Body.SSHKeys, IPv4: in.Body.IPv4}); err != nil {
		return nil, problem(err)
	}
	return h.state(ctx, nil)
}

func (h *HTTP) powerOff(ctx context.Context, in *tokenIn) (*struct{}, error) {
	if err := h.active(); err != nil {
		return nil, err
	}
	if err := h.s.PowerOff(ctx, in.Token); err != nil {
		return nil, problem(err)
	}
	return nil, nil
}
