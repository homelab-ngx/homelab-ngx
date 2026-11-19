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
}

func problem(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return httpx.BadRequest("invalid_setting", "%s", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	case errors.Is(err, ErrManaged):
		return httpx.Conflict("managed_by_nixos", "%s", err)
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
