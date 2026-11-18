package config

// Security is what the host's NixOS configuration says about its own
// exposure and access, for the security audit: facts the daemon cannot read
// at run time. module.nix fills it from the evaluated configuration; every
// part may be empty on a host that does not use it. It holds no keys.
type Security struct {
	Firewall        Firewall     `json:"firewall"`
	SSH             SSH          `json:"ssh"`
	Users           []User       `json:"users"`
	Sudo            Sudo         `json:"sudo"`
	NixTrustedUsers []string     `json:"nix_trusted_users"`
	WireGuard       []WireGuard  `json:"wireguard"`
	Tailscale       Tailscale    `json:"tailscale"`
	CaddyRoutes     []CaddyRoute `json:"caddy_routes"`
	// Notes are facts the operator states about the host that no option
	// carries, such as who can reach the dashboard.
	Notes []string `json:"notes"`
}

// Firewall is the NixOS firewall's openings.
type Firewall struct {
	Enabled           bool                     `json:"enabled"`
	TCPPorts          []int                    `json:"tcp_ports"`
	UDPPorts          []int                    `json:"udp_ports"`
	Interfaces        map[string]FirewallPorts `json:"interfaces"`
	TrustedInterfaces []string                 `json:"trusted_interfaces"`
	ExtraCommands     string                   `json:"extra_commands"`
}

// FirewallPorts are the ports opened on one interface.
type FirewallPorts struct {
	TCPPorts []int `json:"tcp_ports"`
	UDPPorts []int `json:"udp_ports"`
}

// SSH is the OpenSSH server.
type SSH struct {
	Enabled      bool           `json:"enabled"`
	Ports        []int          `json:"ports"`
	OpenFirewall bool           `json:"open_firewall"`
	Settings     map[string]any `json:"settings"`
}

// User is a normal (login) user.
type User struct {
	Name    string   `json:"name"`
	Groups  []string `json:"groups"`
	SSHKeys int      `json:"ssh_keys"`
}

// Sudo is how sudo is set up.
type Sudo struct {
	Enabled            bool `json:"enabled"`
	WheelNeedsPassword bool `json:"wheel_needs_password"`
}

// WireGuard is one WireGuard interface and its peers.
type WireGuard struct {
	Interface  string          `json:"interface"`
	ListenPort int             `json:"listen_port"`
	Peers      []WireGuardPeer `json:"peers"`
}

// WireGuardPeer is a peer by name and the addresses it may use.
type WireGuardPeer struct {
	Name       string   `json:"name"`
	AllowedIPs []string `json:"allowed_ips"`
}

// Tailscale is the Tailscale client.
type Tailscale struct {
	Enabled         bool     `json:"enabled"`
	Port            int      `json:"port"`
	OpenFirewall    bool     `json:"open_firewall"`
	RoutingFeatures string   `json:"routing_features"`
	SetFlags        []string `json:"set_flags"`
	UpFlags         []string `json:"up_flags"`
}

// CaddyRoute is one site of the host's own Caddy, outside the stacks'.
type CaddyRoute struct {
	Name            string   `json:"name"`
	Host            string   `json:"host"`
	Aliases         []string `json:"aliases"`
	Upstream        string   `json:"upstream"`
	Directives      string   `json:"directives"`
	ProxyDirectives string   `json:"proxy_directives"`
	Static          bool     `json:"static"`
}
