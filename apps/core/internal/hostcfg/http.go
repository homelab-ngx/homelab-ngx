package hostcfg

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
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
type ddnsIn struct{ Body DDNSSet }
type smtpIn struct{ Body SMTPSet }
type timeIn struct{ Body Time }
type consoleIn struct{ Body Console }
type layoutsOut struct{ Body KeyboardLayouts }
type deviceIn struct{ Body DeviceAdd }
type deviceOut struct{ Body AddedDevice }
type publicAddressOut struct{ Body PublicAddress }

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
	httpx.Register(a, httpx.ScopeOperator, put("setHostTime", "time", "Set the clock's format and its time servers."), h.setTime)
	httpx.Register(a, httpx.ScopeOperator, put("setHostConsole", "console", "Set the console's keyboard layout. The console restarts with it."), h.setConsole)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getKeyboardLayouts", Method: http.MethodGet, Path: "/host-settings/keyboard-layouts", Tags: tags,
		Summary: "The keyboard layouts the console can use.",
	}, h.keyboardLayouts)
	httpx.Register(a, httpx.ScopeOperator, put("setHostSSH", "ssh", "Set root's authorized SSH keys."), h.setSSH)
	httpx.Register(a, httpx.ScopeOperator, put("setHostDomain", "domain", "Set the private domain and its certificate."), h.setDomain)
	httpx.Register(a, httpx.ScopeOperator, put("setHostNetwork", "network", "Set the LAN's subnets."), h.setNetwork)
	httpx.Register(a, httpx.ScopeOperator, put("setHostFirewall", "firewall", "Set the firewall's openings."), h.setFirewall)
	httpx.Register(a, httpx.ScopeOperator, put("setHostWireGuard", "wireguard", "Set the WireGuard VPN and its peers."), h.setWireGuard)
	httpx.Register(a, httpx.ScopeOperator, put("setHostTunnel", "tunnel", "Set the Cloudflare tunnel and its routes."), h.setTunnel)
	httpx.Register(a, httpx.ScopeOperator, put("setHostTailscale", "tailscale", "Set Tailscale."), h.setTailscale)
	httpx.Register(a, httpx.ScopeOperator, put("setHostDDNS", "ddns", "Set dynamic DNS: a name under the domain kept pointed at the home's public address, through the domain's DNS provider."), h.setDDNS)
	httpx.Register(a, httpx.ScopeOperator, put("setHostSMTP", "smtp", "Set the host SMTP relay for stacks."), h.setSMTP)

	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "addWireGuardDevice", Method: http.MethodPost, Path: "/host-settings/wireguard/peers", Tags: tags, Errors: errs,
		Summary:     "Add a VPN device with keys the host makes.",
		Description: "The host makes the device's key pair, gives it the next free address in the VPN and adds it as a peer. The answer's config holds the device's private key, which is not kept and not shown again. `400` while the VPN is off or has no endpoint.",
	}, h.addDevice)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "getPublicAddress", Method: http.MethodGet, Path: "/host-settings/wireguard/public-address", Tags: tags, Errors: []int{http.StatusBadGateway},
		Summary:     "The home's public IPv4 address, for the VPN's endpoint.",
		Description: "Asks Cloudflare (https://1.1.1.1/cdn-cgi/trace) on each call; nothing is kept.",
	}, h.publicAddress)

	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getTailscaleStatus", Method: http.MethodGet, Path: "/host-settings/tailscale/status", Tags: tags,
		Summary: "The host on its tailnet: state, identity, routes, key expiry and health.",
	}, h.tailscaleStatus)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getTailscalePeers", Method: http.MethodGet, Path: "/host-settings/tailscale/peers", Tags: tags,
		Summary: "The other devices on the host's tailnet.",
	}, h.tailscalePeers)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "loginTailscale", Method: http.MethodPost, Path: "/host-settings/tailscale/login", Tags: tags, Errors: append([]int{http.StatusBadRequest}, loginErrs...),
		Summary:     "Turn Tailscale on and start its login.",
		Description: "Starts browser login by default and answers with auth_url once printed (within 15 s). Optional auth_key or login_server logs in directly. Once approved, settings are applied again. `409 managed_by_nixos` on a NixOS host.",
	}, h.tailscaleLogin)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "logoutTailscale", Method: http.MethodPost, Path: "/host-settings/tailscale/logout", Tags: tags, Errors: loginErrs,
		Summary: "Take the host off its tailnet.",
	}, h.tailscaleLogout)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getTunnelStatus", Method: http.MethodGet, Path: "/host-settings/tunnel/status", Tags: tags,
		Summary: "Live Cloudflare tunnel connector health and per-route DNS and Access exposure.",
	}, h.tunnelStatus)
	httpx.Register(a, httpx.ScopeControl, huma.Operation{
		OperationID: "restartTunnel", Method: http.MethodPost, Path: "/host-settings/tunnel/restart", Tags: tags, Errors: []int{http.StatusBadRequest, http.StatusConflict, http.StatusBadGateway},
		Summary: "Restart cloudflared and report its new status.",
	}, h.tunnelRestart)
	httpx.Register(a, httpx.ScopeControl, huma.Operation{
		OperationID: "checkTunnelRoute", Method: http.MethodPost, Path: "/host-settings/tunnel/routes/{hostname}/check", Tags: tags, Errors: []int{http.StatusBadRequest},
		Summary: "Probe one published tunnel route now and return the updated tunnel status.",
	}, h.checkTunnelRoute)
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
		OperationID: "cancelTunnelLogin", Method: http.MethodPost, Path: "/host-settings/tunnel/login/cancel", Tags: tags, Errors: []int{http.StatusConflict},
		Summary: "Abort a waiting Cloudflare login and return to idle.",
	}, h.tunnelLoginCancel)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "forgetTunnel", Method: http.MethodPost, Path: "/host-settings/tunnel/forget", Tags: tags, Errors: errs,
		Summary: "Drop the tunnel's credentials and turn it off. The tunnel and its DNS records stay at Cloudflare.",
	}, h.tunnelForget)
}

type tailscaleOut struct{ Body TailscaleStatus }
type tailscalePeersOut struct{ Body TailscalePeers }
type tailscaleLoginIn struct {
	Body *TailscaleLoginInput `required:"false"`
}
type loginOut struct{ Body Login }
type tunnelStatusOut struct{ Body TunnelStatus }
type routeCheckIn struct {
	Hostname string `path:"hostname" doc:"The route's public hostname."`
}

func (h *HTTP) tailscaleStatus(ctx context.Context, _ *struct{}) (*tailscaleOut, error) {
	return &tailscaleOut{Body: h.s.TailscaleStatus(ctx)}, nil
}
func (h *HTTP) tailscalePeers(ctx context.Context, _ *struct{}) (*tailscalePeersOut, error) {
	return &tailscalePeersOut{Body: h.s.TailscalePeers(ctx)}, nil
}
func (h *HTTP) tailscaleLogin(ctx context.Context, in *tailscaleLoginIn) (*tailscaleOut, error) {
	var body TailscaleLoginInput
	if in != nil && in.Body != nil {
		body = *in.Body
	}
	st, err := h.s.TailscaleLogin(ctx, body)
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
func (h *HTTP) tunnelStatus(ctx context.Context, _ *struct{}) (*tunnelStatusOut, error) {
	return &tunnelStatusOut{Body: h.s.TunnelStatus(ctx)}, nil
}
func (h *HTTP) tunnelRestart(ctx context.Context, _ *struct{}) (*tunnelStatusOut, error) {
	st, err := h.s.TunnelRestart(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &tunnelStatusOut{Body: st}, nil
}
func (h *HTTP) checkTunnelRoute(ctx context.Context, in *routeCheckIn) (*tunnelStatusOut, error) {
	st, err := h.s.RecheckTunnelRoute(ctx, in.Hostname)
	if err != nil {
		return nil, problem(err)
	}
	return &tunnelStatusOut{Body: st}, nil
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
func (h *HTTP) tunnelLoginCancel(_ context.Context, _ *struct{}) (*loginOut, error) {
	l, err := h.s.TunnelLoginCancel()
	if err != nil {
		return nil, problem(err)
	}
	return &loginOut{Body: l}, nil
}
func (h *HTTP) setDDNS(ctx context.Context, in *ddnsIn) (*settingsOut, error) {
	return out(h.s.SetDDNS(ctx, in.Body))
}
func (h *HTTP) setSMTP(ctx context.Context, in *smtpIn) (*settingsOut, error) {
	return out(h.s.SetSMTP(ctx, in.Body))
}
func (h *HTTP) addDevice(ctx context.Context, in *deviceIn) (*deviceOut, error) {
	d, err := h.s.AddWireGuardDevice(ctx, in.Body)
	if err != nil {
		return nil, problem(err)
	}
	ifs, _ := net.Interfaces()
	d.Settings.Wired = wired(ifs)
	return &deviceOut{Body: d}, nil
}
func (h *HTTP) publicAddress(ctx context.Context, _ *struct{}) (*publicAddressOut, error) {
	a, err := h.s.PublicAddress(ctx)
	if err != nil {
		return nil, problem(err)
	}
	return &publicAddressOut{Body: a}, nil
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
	case errors.Is(err, ErrUpstream):
		return httpx.BadGateway("upstream_failed", "%s", strings.TrimPrefix(err.Error(), ErrUpstream.Error()+": "))
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
		iface := Interface{Name: i.Name, MAC: i.HardwareAddr.String(), Addresses: []string{}, Subnets: []string{}}
		// Index 0 would list every interface's addresses.
		if addrs, err := i.Addrs(); err == nil && i.Index > 0 {
			for _, a := range addrs {
				if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() {
					iface.Addresses = append(iface.Addresses, p.Addr().String())
					if sub := p.Masked().String(); p.Bits() < 32 && !slices.Contains(iface.Subnets, sub) {
						iface.Subnets = append(iface.Subnets, sub)
					}
				}
			}
		}
		out = append(out, iface)
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
func (h *HTTP) setTime(ctx context.Context, in *timeIn) (*settingsOut, error) {
	return out(h.s.SetTime(ctx, in.Body))
}
func (h *HTTP) setConsole(ctx context.Context, in *consoleIn) (*settingsOut, error) {
	return out(h.s.SetConsole(ctx, in.Body))
}
func (h *HTTP) keyboardLayouts(_ context.Context, _ *struct{}) (*layoutsOut, error) {
	return &layoutsOut{Body: h.s.KeyboardLayouts()}, nil
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
