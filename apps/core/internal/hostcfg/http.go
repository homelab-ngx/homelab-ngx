package hostcfg

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"homelab-ngx.com/core/internal/httpx"
)

// HTTP is the host settings slice of the API.
type HTTP struct{ s *Service }

// NewHTTP returns the HTTP adapter for s.
func NewHTTP(s *Service) *HTTP { return &HTTP{s: s} }

type settingsOut struct{ Body HostSettings }
type hostIn struct{ Body Host }
type sshIn struct{ Body SSHKeys }
type domainIn struct{ Body DomainSet }
type networkIn struct{ Body Network }
type firewallIn struct{ Body Firewall }
type wireguardIn struct{ Body WireGuard }
type tunnelIn struct{ Body TunnelSet }
type tailscaleIn struct{ Body Tailscale }

// Register adds the operations: reading is read, changing is operator.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"host"}
	errs := []int{http.StatusBadRequest, http.StatusConflict}
	loginErrs := []int{http.StatusConflict, http.StatusBadGateway}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getHostSettings", Method: http.MethodGet, Path: "/host-settings", Tags: tags,
		Summary: "The host's settings, without secrets, and the last apply's outcome.",
	}, h.get)
	put := func(id, section, summary string) huma.Operation {
		return huma.Operation{
			OperationID: id, Method: http.MethodPut, Path: "/host-settings/" + section, Tags: tags, Summary: summary,
			Description: "Applied at once on an image host. `409 managed_by_nixos` on a NixOS host.", Errors: errs,
		}
	}
	httpx.Register(a, httpx.ScopeOperator, put("setHostBasics", "host", "Set the host name and time zone."), h.setHost)
	httpx.Register(a, httpx.ScopeOperator, put("setHostSSH", "ssh", "Set root's authorized SSH keys."), h.setSSH)
	httpx.Register(a, httpx.ScopeOperator, put("setHostDomain", "domain", "Set the private domain and its certificate."), h.setDomain)
	httpx.Register(a, httpx.ScopeOperator, put("setHostNetwork", "network", "Set the LAN's subnets."), h.setNetwork)
	httpx.Register(a, httpx.ScopeOperator, put("setHostFirewall", "firewall", "Set the firewall's openings."), h.setFirewall)
	httpx.Register(a, httpx.ScopeOperator, put("setHostWireGuard", "wireguard", "Set the WireGuard VPN and its peers."), h.setWireGuard)
	httpx.Register(a, httpx.ScopeOperator, put("setHostTunnel", "tunnel", "Set the Cloudflare tunnel and its routes."), h.setTunnel)
	httpx.Register(a, httpx.ScopeOperator, put("setHostTailscale", "tailscale", "Set Tailscale."), h.setTailscale)

	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getTailscaleStatus", Method: http.MethodGet, Path: "/host-settings/tailscale/status", Tags: tags,
		Summary: "The host on its tailnet: logged in or not, its link while a login waits, its addresses.",
	}, h.tailscaleStatus)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "loginTailscale", Method: http.MethodPost, Path: "/host-settings/tailscale/login", Tags: tags, Errors: loginErrs,
		Summary:     "Turn Tailscale on and start its login.",
		Description: "Answers with auth_url once tailscale login prints it (within 15 s). The link stays open for 10 minutes; once the host is approved, the settings are applied again. `409 managed_by_nixos` on a NixOS host.",
	}, h.tailscaleLogin)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "logoutTailscale", Method: http.MethodPost, Path: "/host-settings/tailscale/logout", Tags: tags, Errors: loginErrs,
		Summary: "Take the host off its tailnet.",
	}, h.tailscaleLogout)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getTunnelLogin", Method: http.MethodGet, Path: "/host-settings/tunnel/login", Tags: tags,
		Summary: "The last Cloudflare login: its link while it waits, and how it ended.",
	}, h.tunnelLoginState)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "loginTunnel", Method: http.MethodPost, Path: "/host-settings/tunnel/login", Tags: tags, Errors: loginErrs,
		Summary:     "Connect the tunnel through Cloudflare's login.",
		Description: "Runs cloudflared tunnel login and answers with its link once printed (within 15 s). Once a zone is approved there, the host creates its tunnel (or takes back the one named after it), keeps its credentials and turns it on, and adds each route's DNS record. The link stays open for 10 minutes. `409 managed_by_nixos` on a NixOS host.",
	}, h.tunnelLogin)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "forgetTunnel", Method: http.MethodPost, Path: "/host-settings/tunnel/forget", Tags: tags, Errors: errs,
		Summary: "Drop the tunnel's credentials and turn it off. The tunnel and its DNS records stay at Cloudflare.",
	}, h.tunnelForget)
}

type tailscaleOut struct{ Body TailscaleStatus }
type loginOut struct{ Body Login }

func (h *HTTP) tailscaleStatus(ctx context.Context, _ *struct{}) (*tailscaleOut, error) {
	return &tailscaleOut{Body: h.s.TailscaleStatus(ctx)}, nil
}
func (h *HTTP) tailscaleLogin(ctx context.Context, _ *struct{}) (*tailscaleOut, error) {
	st, err := h.s.TailscaleLogin(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &tailscaleOut{Body: st}, nil
}
func (h *HTTP) tailscaleLogout(ctx context.Context, _ *struct{}) (*tailscaleOut, error) {
	st, err := h.s.TailscaleLogout(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &tailscaleOut{Body: st}, nil
}
func (h *HTTP) tunnelLoginState(_ context.Context, _ *struct{}) (*loginOut, error) {
	return &loginOut{Body: h.s.TunnelLoginState()}, nil
}
func (h *HTTP) tunnelLogin(ctx context.Context, _ *struct{}) (*loginOut, error) {
	l, err := h.s.TunnelLogin(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &loginOut{Body: l}, nil
}
func (h *HTTP) tunnelForget(ctx context.Context, _ *struct{}) (*settingsOut, error) {
	return out(h.s.TunnelForget(ctx))
}

func problem(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return httpx.BadRequest("invalid_setting", "%s", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	case errors.Is(err, ErrManaged):
		return httpx.Conflict("managed_by_nixos", "%s", err)
	case errors.Is(err, ErrCommand):
		return httpx.BadGateway("command_failed", "%s", strings.TrimPrefix(err.Error(), ErrCommand.Error()+": "))
	}
	return err
}

func out(st HostSettings, err error) (*settingsOut, error) {
	if err != nil {
		return nil, problem(err)
	}
	ifs, _ := net.Interfaces()
	st.Wired = wired(ifs)
	return &settingsOut{Body: st}, nil
}

// wired picks the interfaces the image's wired network covers (en*, eth*:
// 80-wired.network) that have a MAC address.
func wired(ifs []net.Interface) []Interface {
	out := []Interface{}
	for _, i := range ifs {
		if len(i.HardwareAddr) == 0 || !(strings.HasPrefix(i.Name, "en") || strings.HasPrefix(i.Name, "eth")) {
			continue
		}
		out = append(out, Interface{Name: i.Name, MAC: i.HardwareAddr.String()})
	}
	return out
}

func (h *HTTP) get(ctx context.Context, _ *struct{}) (*settingsOut, error) { return out(h.s.Get(ctx)) }
func (h *HTTP) setSSH(ctx context.Context, in *sshIn) (*settingsOut, error) {
	return out(h.s.SetSSHKeys(ctx, in.Body))
}
func (h *HTTP) setHost(ctx context.Context, in *hostIn) (*settingsOut, error) {
	return out(h.s.SetHost(ctx, in.Body))
}
func (h *HTTP) setDomain(ctx context.Context, in *domainIn) (*settingsOut, error) {
	return out(h.s.SetDomain(ctx, in.Body))
}
func (h *HTTP) setNetwork(ctx context.Context, in *networkIn) (*settingsOut, error) {
	return out(h.s.SetNetwork(ctx, in.Body))
}
func (h *HTTP) setFirewall(ctx context.Context, in *firewallIn) (*settingsOut, error) {
	return out(h.s.SetFirewall(ctx, in.Body))
}
func (h *HTTP) setWireGuard(ctx context.Context, in *wireguardIn) (*settingsOut, error) {
	return out(h.s.SetWireGuard(ctx, in.Body))
}
func (h *HTTP) setTunnel(ctx context.Context, in *tunnelIn) (*settingsOut, error) {
	return out(h.s.SetTunnel(ctx, in.Body))
}
func (h *HTTP) setTailscale(ctx context.Context, in *tailscaleIn) (*settingsOut, error) {
	return out(h.s.SetTailscale(ctx, in.Body))
}
