// Package hostcfg keeps the image host's own settings (#96): host name and
// time zone, domain and HTTPS, LAN, firewall openings, the WireGuard VPN,
// the Cloudflare tunnel's routes and Tailscale. They live in SQLite, secrets
// sealed to the host's age key, and the daemon writes what the host reads
// from them (apply.go). A NixOS host shows the values of its NixOS
// configuration instead, read-only.
package hostcfg

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Managed says who owns a host's settings.
type Managed string

const (
	// ManagedImage: the daemon keeps and applies them.
	ManagedImage Managed = "image"
	// ManagedNixOS: the NixOS configuration does; the API is read-only.
	ManagedNixOS Managed = "nixos"
)

// Schema documents Managed as its enum.
func (Managed) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{string(ManagedImage), string(ManagedNixOS)}}
}

// HostSettings is every section as the API shows it: no secrets.
type HostSettings struct {
	Managed   Managed   `json:"managed" doc:"image: set here and applied by the daemon. nixos: from the host's NixOS configuration, read-only."`
	Host      Host      `json:"host"`
	Domain    Domain    `json:"domain"`
	Network   Network   `json:"network"`
	Firewall  Firewall  `json:"firewall"`
	WireGuard WireGuard `json:"wireguard"`
	Tunnel    Tunnel    `json:"tunnel"`
	Tailscale Tailscale `json:"tailscale"`
	// Applied is when the daemon last wrote the host's files, and what failed.
	Applied Applied `json:"applied"`
}

// Applied is the last apply's outcome.
type Applied struct {
	At     *time.Time `json:"at,omitempty"`
	Errors []string   `json:"errors" doc:"What could not be applied; empty when everything was."`
}

// Host is the host's name and time zone.
type Host struct {
	Hostname string `required:"false" json:"hostname" doc:"One label: letters, digits and -. Empty keeps the image's (homelab)."`
	Timezone string `required:"false" json:"timezone" doc:"An IANA zone such as Europe/Stockholm; empty is UTC."`
	// Set with setHostSSH; setHostBasics keeps them.
	SSHKeys []string `required:"false" json:"ssh_keys,omitempty" doc:"Root's authorized SSH public keys (OpenSSH format), set with setHostSSH."`
}

// SSHKeys is root's authorized SSH public keys.
type SSHKeys struct {
	Keys []string `json:"keys" doc:"One public key per entry, in OpenSSH format (ssh-ed25519 AAAA… comment)."`
}

// DNSProvider is the DNS-01 provider Caddy proves the domain with.
type DNSProvider string

const (
	DNSNone       DNSProvider = ""
	DNSCloudflare DNSProvider = "cloudflare"
	DNSPorkbun    DNSProvider = "porkbun"
	DNSDuckDNS    DNSProvider = "duckdns"
	DNSRoute53    DNSProvider = "route53"
)

// Schema documents DNSProvider as its enum.
func (DNSProvider) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Enum: []any{"", "cloudflare", "porkbun", "duckdns", "route53"}}
}

// dnsFields are each provider's credential fields, all secret.
var dnsFields = map[DNSProvider][]string{
	DNSCloudflare: {"api_token"},
	DNSPorkbun:    {"api_key", "api_secret_key"},
	DNSDuckDNS:    {"api_token"},
	DNSRoute53:    {"access_key_id", "secret_access_key"},
}

// Domain is the private domain the dashboard and the stacks are served
// under, with a wildcard certificate.
type Domain struct {
	Domain string `json:"domain" doc:"The private domain, such as home.example.com: the dashboard and every stack route are served under it. Empty: the dashboard on plain HTTP at the host's address."`
	// DashboardLabel is the dashboard's name under the domain.
	DashboardLabel string      `json:"dashboard_label" doc:"The dashboard's name under the domain; empty is homelab."`
	ACMEEmail      string      `json:"acme_email" doc:"Contact for the certificate authority; optional."`
	DNSProvider    DNSProvider `json:"dns_provider" doc:"Where the domain's DNS is hosted, for a wildcard certificate by DNS-01. Empty: a certificate from Caddy's own local authority."`
	// DNSCredentialsSet lists the credential fields stored for the provider.
	DNSCredentialsSet bool   `json:"dns_credentials_set" doc:"The provider's credentials are stored; they are never shown."`
	Region            string `json:"region,omitempty" doc:"route53 only: the AWS region."`
}

// DomainSet changes the domain section.
type DomainSet struct {
	Domain         string      `json:"domain" maxLength:"253"`
	DashboardLabel string      `json:"dashboard_label,omitempty" maxLength:"63"`
	ACMEEmail      string      `json:"acme_email,omitempty" maxLength:"254"`
	DNSProvider    DNSProvider `json:"dns_provider,omitempty"`
	Region         string      `json:"region,omitempty" maxLength:"64"`
	// DNSCredentials: absent keeps the stored ones (dropped when the
	// provider changes).
	DNSCredentials map[string]string `json:"dns_credentials,omitempty" doc:"The provider's fields: cloudflare and duckdns api_token; porkbun api_key and api_secret_key; route53 access_key_id and secret_access_key. Absent keeps the stored ones."`
}

// Network is what the host calls its LAN.
type Network struct {
	LAN  []string `json:"lan" doc:"The LAN's IPv4 subnets, such as 192.168.1.0/24: the clients a stack's port grant from lan lets through."`
	IPv4 IPv4     `required:"false" json:"ipv4" doc:"The wired interfaces' IPv4 address: from DHCP, or static."`
}

// IPv4 is how the host gets its address on the wired interfaces.
type IPv4 struct {
	Mode    string   `json:"mode" enum:"dhcp,static" doc:"dhcp (the default) or static."`
	Address string   `required:"false" json:"address,omitempty" doc:"With static: the address and prefix, such as 192.168.1.20/24."`
	Gateway string   `required:"false" json:"gateway,omitempty" doc:"With static: the default gateway."`
	DNS     []string `required:"false" json:"dns,omitempty" doc:"With static: up to three DNS servers."`
}

// Opening is one port the host firewall lets in.
type Opening struct {
	Port  int      `json:"port" minimum:"1" maximum:"65535"`
	Proto string   `json:"proto" enum:"tcp,udp"`
	From  []string `required:"false" json:"from" doc:"IPv4 subnets allowed; empty is anyone."`
	Note  string   `json:"note,omitempty" maxLength:"200"`
}

// Firewall is the host firewall's openings. SSH (22), HTTP and HTTPS (80,
// 443), the VPN's port and DHCP are always open; everything else that is
// not answered traffic is dropped.
type Firewall struct {
	Openings []Opening `json:"openings"`
}

// Peer is one WireGuard client.
type Peer struct {
	Name      string `json:"name" minLength:"1" maxLength:"64"`
	PublicKey string `json:"public_key" doc:"The client's public key (base64)."`
	Address   string `json:"address" doc:"The client's address in the VPN, such as 10.10.0.2/32."`
}

// WireGuard is the host's VPN server for its own devices.
type WireGuard struct {
	Enabled    bool   `json:"enabled"`
	ListenPort int    `required:"false" json:"listen_port" doc:"UDP port; 0 is 51820."`
	Address    string `required:"false" json:"address" doc:"The host's address and the VPN's subnet, such as 10.10.0.1/24."`
	Endpoint   string `required:"false" json:"endpoint" doc:"How clients reach the host from outside, such as vpn.example.com:51820, for their configs."`
	// PublicKey is the server's; its private key is made on first enable.
	PublicKey string `json:"public_key,omitempty" doc:"The host's public key, for client configs. Made when the VPN is first turned on."`
	Peers     []Peer `json:"peers"`
}

// Route is one public hostname the tunnel serves from a private one.
type Route struct {
	Hostname string `json:"hostname" doc:"The public hostname on Cloudflare, such as photos.example.net."`
	Target   string `json:"target" doc:"What it serves: a name under the private domain (photos) or a full host name."`
	Path     string `json:"path,omitempty" doc:"Only paths matching this regular expression, such as ^/opds; empty is every path."`
}

// Tunnel is the Cloudflare tunnel publishing some private routes.
type Tunnel struct {
	Enabled        bool    `json:"enabled"`
	TunnelID       string  `json:"tunnel_id,omitempty" doc:"From the stored credentials."`
	CredentialsSet bool    `json:"credentials_set" doc:"The tunnel's credentials file is stored; it is never shown."`
	Routes         []Route `json:"routes"`
}

// TunnelSet changes the tunnel section.
type TunnelSet struct {
	Enabled bool    `json:"enabled"`
	Routes  []Route `json:"routes" required:"false"`
	// Credentials: absent keeps the stored ones.
	Credentials *string `json:"credentials,omitempty" maxLength:"4096" doc:"The tunnel's credentials file (JSON with AccountTag, TunnelID and TunnelSecret), from cloudflared tunnel create. Absent keeps the stored one."`
}

// Tailscale is the host on a tailnet.
type Tailscale struct {
	Enabled         bool     `json:"enabled" doc:"Runs tailscaled; log it in once with tailscale up on the host."`
	AdvertiseRoutes []string `required:"false" json:"advertise_routes" doc:"Subnets offered to the tailnet, such as the LAN."`
}
