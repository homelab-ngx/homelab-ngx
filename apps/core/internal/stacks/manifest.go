package stacks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"homelab-ngx.com/core/internal/storage"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
)

// ManifestFile is the manifest's path in a stack repo.
const ManifestFile = "homelab-stack.json"

// ManifestVersion is the newest manifest version this daemon reads. Version
// 2 adds ports and containers to version 1; version 3 adds api; version 4
// adds secrets; version 5 replaces secrets and storage with variables;
// version 6 adds backup; version 7 adds nixpkgs, builds the repo's
// nixosModules.default on the daemon's base module, and gives env variables
// a type and allowed values instead of a default; version 8 adds route
// labels under the host's domain, address variables that egress and port
// clients can name, the owner and group of a path, and the GPU and firewall
// from the host.
const ManifestVersion = 8

// MinBuildVersion is the oldest manifest a stack is built from. Older
// manifests still parse, since stored generations carry them.
const MinBuildVersion = 7

// FlakeFile is the flake a version 7 repo exposes its module in.
const FlakeFile = "flake.nix"

// Manifest is what a stack repo declares about itself and asks of the host.
// Routes, storage, path variables, GPU, egress, ports, containers and API
// scopes are grants the operator approves; the health check, secrets, env
// variables and the backup section are not. The
// system to build is not a choice: up to version 6 the repo's flake must
// expose nixosConfigurations.<Name>; from version 7 it exposes
// nixosModules.default, which the daemon builds on its base module with the
// nixpkgs of Nixpkgs. Name is the repo's own name: it suggests the name a
// stack is installed under, which the operator may choose instead.
type Manifest struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	// Nixpkgs (version 7) is the nixpkgs the repo recommends: a channel
	// (nixos-unstable, nixos-YY.MM) or a NixOS/nixpkgs commit.
	Nixpkgs string    `json:"nixpkgs,omitempty"`
	Routes  []Route   `json:"routes"`
	Storage []Storage `json:"storage"`
	GPU     bool      `json:"gpu"`
	Egress  []Egress  `json:"egress"`
	Ports   []Port    `json:"ports"`
	// Containers lets the stack run podman, which needs the bpf, add_key
	// and keyctl system calls nspawn refuses by default.
	Containers bool   `json:"containers"`
	Health     Health `json:"health"`
	// API is the daemon API scopes (APIRead, APIControl) the stack may call
	// on the stack listener with its token. Absent when it has none, so a
	// manifest without it encodes as it did before version 3.
	API []string `json:"api,omitempty"`
	// Secrets are the stack variables the stack needs a value for. The
	// operator fills them in on the host; a build waits until every one has
	// a value. Not a grant: they ask nothing of the host. Absent when there
	// are none, so a manifest without them encodes as it did before
	// version 4.
	Secrets []Secret `json:"secrets,omitempty"`
	// Variables (version 5) are the values the stack gets from the host:
	// env and secret values it reads at /run/secrets, and paths, host
	// directories the operator picks that are bound at their mount. They
	// replace secrets and storage. Absent when there are none.
	Variables []Variable `json:"variables,omitempty"`
	// Backup (version 6) is what the stack recommends backing up and the
	// unit that prepares it. Not a grant: nothing is backed up until the
	// operator adds a folder to a backup. Absent when there is none.
	Backup *Backup `json:"backup,omitempty"`
}

// Backup is a version 6 manifest's backup section.
type Backup struct {
	// Prepare is a oneshot unit inside the stack (its dump job) that is
	// started and waited for before the stack's folders are copied.
	Prepare string `json:"prepare,omitempty"`
	// Folders are the directories worth backing up.
	Folders []BackupDir `json:"folders,omitempty"`
}

// BackupDir is one recommended folder: a path inside the stack, or the host
// directory of one of its path variables.
type BackupDir struct {
	Label    string         `json:"label"`
	Path     string         `json:"path,omitempty"`
	Variable string         `json:"variable,omitempty"`
	Exclude  []BackupIgnore `json:"exclude,omitempty"`
}

// Key names the folder within its stack: its path, or its variable's name.
// A backup refers to the folder by it, so a folder whose path changes is a
// new folder.
func (d BackupDir) Key() string {
	if d.Variable != "" {
		return d.Variable
	}
	return d.Path
}

// BackupIgnore is a pattern left out of a recommended folder, and why.
type BackupIgnore struct {
	Pattern string `json:"pattern"`
	Reason  string `json:"reason"`
}

// BackupDirs are the recommended folders m declares.
func (m Manifest) BackupDirs() []BackupDir {
	if m.Backup == nil {
		return nil
	}
	return m.Backup.Folders
}

// Secret is one secret a version 4 manifest declares by name. The value
// never comes from the repo.
type Secret struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Variable kinds.
const (
	// VarEnv is a plain value.
	VarEnv = "env"
	// VarSecret is a write-only value. It has no default.
	VarSecret = "secret"
	// VarPath is a host directory bound into the stack at Mount.
	VarPath = "path"
	// VarAddress (version 8) is a list of IPv4 addresses or CIDRs the
	// operator gives: an egress destination or the clients of a port grant
	// that name it ($NAME) take their addresses from it, and the stack reads
	// it like an env value.
	VarAddress = "address"
)

// Variable is one value a version 5 manifest declares. Its value never comes
// from the repo; a default is what an env or path variable resolves to while
// the operator has stored none.
type Variable struct {
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
	Required    bool    `json:"required"`
	Default     *string `json:"default,omitempty"`
	// Mount and ReadOnly are a path's bind inside the stack.
	Mount    string `json:"mount,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
	// Type (version 7, env only) is what the value is, for the dashboard's
	// input: string, number or boolean. Values are not checked against it.
	Type string `json:"type,omitempty"`
	// Enum (version 7, env only) lists the values allowed; a value outside
	// it is refused.
	Enum []string `json:"enum,omitempty"`
	// Owner and Group (version 8, path only) name a user and a group of the
	// stack that take the uid and gid of the host directory, so the stack
	// pins no host ids. The stack declares the user or group itself.
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// Types of an env variable (version 7).
const (
	TypeString  = "string"
	TypeNumber  = "number"
	TypeBoolean = "boolean"
)

// maxEnum bounds a variable's allowed values.
const maxEnum = 64

// Vars are the variables m declares. A version 4 secret reads as a required
// secret; storage stays a fixed bind and is not a variable.
func (m Manifest) Vars() []Variable {
	if len(m.Secrets) == 0 {
		return m.Variables
	}
	out := make([]Variable, 0, len(m.Secrets))
	for _, sec := range m.Secrets {
		out = append(out, Variable{Name: sec.Name, Kind: VarSecret, Description: sec.Description, Required: true})
	}
	return out
}

// Variable returns the variable m declares under name.
func (m Manifest) Variable(name string) (Variable, bool) {
	for _, v := range m.Vars() {
		if v.Name == name {
			return v, true
		}
	}
	return Variable{}, false
}

// pathVars are m's path variables, in manifest order.
func (m Manifest) pathVars() []Variable {
	var out []Variable
	for _, v := range m.Variables {
		if v.Kind == VarPath {
			out = append(out, v)
		}
	}
	return out
}

// API scopes a manifest may ask for. They are the scopes of the same name
// the daemon registers its operations with.
const (
	// APIRead is every GET on the stack listener.
	APIRead = "read"
	// APIControl starts, stops and restarts stack units and stacks.
	APIControl = "control"
)

// HasAPI reports whether the manifest grants scope.
func (m Manifest) HasAPI(scope string) bool { return slices.Contains(m.API, scope) }

// Route publishes one hostname through Caddy to a port in the stack. From
// version 8 Host may be a single label, which the host serves under its
// private domain (SetPrivateDomain): "immich" is immich.<domain>.
type Route struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// MaxBody raises Caddy's request body limit, e.g. "50GB". Empty keeps
	// the default.
	MaxBody string `json:"max_body,omitempty"`
}

// Storage binds a host path into the stack. The stack's root has full
// control of a writable path.
type Storage struct {
	HostPath string `json:"host_path"`
	Mount    string `json:"mount"`
	ReadOnly bool   `json:"read_only"`
}

// Egress opens one destination beyond the default (the internet and Caddy
// on 443). To is "host", a CIDR, or (version 8) $NAME, an address variable
// whose value the operator gives.
type Egress struct {
	To    string `json:"to"`
	Port  int    `json:"port"`
	Proto string `json:"proto"`
}

// Port forwards a port on the host's own addresses to a port in the stack,
// for clients in From only: CIDRs and, from version 8, "lan" (the host's
// LAN) and $NAME (an address variable). It is for protocols Caddy cannot carry, such as
// Plex's direct connections or RTSP.
type Port struct {
	Port  int      `json:"port"`
	To    int      `json:"to"`
	Proto string   `json:"proto"`
	From  []string `json:"from"`
}

// Health decides whether a generation is kept after a switch.
type Health struct {
	// Units must all be active.
	Units []string   `json:"units"`
	HTTP  *HTTPProbe `json:"http,omitempty"`
}

// HTTPProbe must answer 2xx on the stack's address.
type HTTPProbe struct {
	Port int    `json:"port"`
	Path string `json:"path"`
}

// EgressHost is the Egress.To value for the host itself.
const EgressHost = "host"

// FromLAN is the Port.From value for the host's LAN.
const FromLAN = "lan"

// varRef is the address variable a version 8 egress destination or port
// client names ($NAME), if it names one.
func varRef(s string) (string, bool) {
	n, ok := strings.CutPrefix(s, "$")
	return n, ok && ValidVariableName(n)
}

// privateDomain is the host's private domain, under which a version 8
// route label is served. The daemon sets it once at startup; without it a
// label stays a label, which is fine for reading a manifest (the CLI) but
// not for serving one.
var privateDomain atomic.Pointer[string]

// SetPrivateDomain sets the domain route labels are served under.
func SetPrivateDomain(d string) {
	d = strings.TrimSuffix(strings.ToLower(d), ".")
	privateDomain.Store(&d)
}

// PrivateDomain is the domain route labels are served under, or "".
func PrivateDomain() string {
	if d := privateDomain.Load(); d != nil {
		return *d
	}
	return ""
}

// IsLabel reports whether a route host is a version 8 label, not a full
// hostname.
func IsLabel(host string) bool { return labelRE.MatchString(host) }

// builtinNames are the variables the host fills in itself.
var builtinNames = []string{BuiltinToken, "HOMELAB_API_URL", BuiltinDomain, BuiltinDashboardURL}

// IsBuiltin reports whether name is a variable the host fills in.
func IsBuiltin(name string) bool { return slices.Contains(builtinNames, name) }

var (
	// A stack name is also the machine name and part of the host interface
	// name `ve-<name>`, which the kernel limits to 15 bytes.
	nameRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,11}$`)
	hostRE  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
	labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// A user or group name inside the stack.
	accountRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	unitRE    = regexp.MustCompile(`^[A-Za-z0-9:_.@-]+\.service$`)
	maxBodyRE = regexp.MustCompile(`^[1-9][0-9]{0,5}(KB|MB|GB)$`)
	// A stack variable's name is also a file name, a URL segment and an
	// environment variable.
	varNameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// maxSecretDescription bounds a declared variable's description.
const maxSecretDescription = 300

// StacksStateDir holds every stack's container root and stored variables:
// appdata/stacks under the host's cache directory (SetCacheDir).
var StacksStateDir = StateDirIn("/var/lib/homelab") // config.DefaultCacheDir

// StateDirIn is the stacks' state directory under the cache directory cache.
func StateDirIn(cache string) string { return path.Join(cache, "appdata", "stacks") }

// SetCacheDir puts the stacks' state under the host's cache directory. The
// daemon and the CLI call it once at start, before any stack is touched.
func SetCacheDir(cache string) { StacksStateDir = StateDirIn(cache) }

// hostPathDenied are host trees no stack may be given: the store, the
// host's own state and configuration, kernel filesystems, and the other
// stacks' state.
func hostPathDenied() []string {
	return []string{StacksStateDir, "/nix", "/etc", "/run", "/var", "/proc", "/sys", "/dev", "/boot", "/root", "/home", "/tmp", "/usr", "/bin"}
}

// portDenied are host ports a stack may not take over: SSH, DNS, and
// Caddy's HTTP and HTTPS.
var portDenied = map[int]bool{22: true, 53: true, 80: true, 443: true}

// mountDenied are paths inside the stack a bind may not cover.
var mountDenied = []string{"/nix", "/etc", "/run", "/proc", "/sys", "/dev", "/usr", "/bin"}

// hostUnitsClash refuses health units that the host would show under one
// name (<stack>-<unit>) when the stack is installed as stack.
func hostUnitsClash(stack string, m Manifest) error {
	hostNames := map[string]bool{}
	for i, u := range m.Health.Units {
		n := hostUnitName(stack, u)
		if hostNames[n] {
			return fmt.Errorf("health.units[%d]: %q and another unit are both shown as %s", i, u, n)
		}
		hostNames[n] = true
	}
	return nil
}

// ValidName reports whether s can name a stack.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ValidVariableName reports whether s can name a stack variable: 1-64
// characters of A-Z, 0-9 and '_', starting with a letter.
func ValidVariableName(s string) bool { return varNameRE.MatchString(s) }

// ParseManifest decodes and validates a manifest. Unknown fields are an
// error: a field this daemon does not know could be a grant it would
// otherwise skip without asking.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return Manifest{}, errors.New("decode: trailing data after the manifest")
	}
	m.normalize()
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// normalize fills defaults so two manifests that mean the same compare equal
// and encode without nulls.
func (m *Manifest) normalize() {
	if m.Routes == nil {
		m.Routes = []Route{}
	}
	if domain := PrivateDomain(); m.Version >= 8 && domain != "" {
		for i := range m.Routes {
			if IsLabel(m.Routes[i].Host) {
				m.Routes[i].Host += "." + domain
			}
		}
	}
	if m.Storage == nil {
		m.Storage = []Storage{}
	}
	if m.Egress == nil {
		m.Egress = []Egress{}
	}
	if m.Ports == nil {
		m.Ports = []Port{}
	}
	if m.Health.Units == nil {
		m.Health.Units = []string{}
	}
	if len(m.API) == 0 {
		m.API = nil
	}
	if len(m.Secrets) == 0 {
		m.Secrets = nil
	}
	if len(m.Variables) == 0 {
		m.Variables = nil
	}
	if b := m.Backup; b != nil {
		if b.Prepare == "" && len(b.Folders) == 0 {
			m.Backup = nil
		} else {
			if len(b.Folders) == 0 {
				b.Folders = nil
			}
			for i := range b.Folders {
				if len(b.Folders[i].Exclude) == 0 {
					b.Folders[i].Exclude = nil
				}
			}
		}
	}
	for i := range m.Ports {
		p := &m.Ports[i]
		if p.To == 0 {
			p.To = p.Port
		}
		if p.Proto == "" {
			p.Proto = "tcp"
		}
		if p.From == nil {
			p.From = []string{}
		}
		for j, c := range p.From {
			if pr, err := netip.ParsePrefix(c); err == nil {
				p.From[j] = pr.Masked().String()
			}
		}
	}
	for i := range m.Egress {
		if m.Egress[i].Proto == "" {
			m.Egress[i].Proto = "tcp"
		}
		if p, err := netip.ParsePrefix(m.Egress[i].To); err == nil {
			m.Egress[i].To = p.Masked().String()
		}
	}
}

// Validate checks every field. The first problem is returned, prefixed with
// the field it is in.
func (m Manifest) Validate() error {
	if m.Version < 1 || m.Version > ManifestVersion {
		return fmt.Errorf("version: %d is not supported (this daemon reads versions 1 to %d)", m.Version, ManifestVersion)
	}
	if m.Version < 2 && (len(m.Ports) > 0 || m.Containers) {
		return errors.New("version: ports and containers need version 2")
	}
	if m.Version < 3 && len(m.API) > 0 {
		return errors.New("version: api needs version 3")
	}
	for i, a := range m.API {
		if a != APIRead && a != APIControl {
			return fmt.Errorf("api[%d]: %q must be %q or %q", i, a, APIRead, APIControl)
		}
		if slices.Contains(m.API[:i], a) {
			return fmt.Errorf("api[%d]: %q is listed twice", i, a)
		}
	}
	if m.Version < 4 && len(m.Secrets) > 0 {
		return errors.New("version: secrets need version 4")
	}
	for i, sec := range m.Secrets {
		if !ValidVariableName(sec.Name) {
			return fmt.Errorf("secrets[%d].name: %q must be 1-64 characters of A-Z, 0-9 and '_', starting with a letter", i, sec.Name)
		}
		if slices.ContainsFunc(m.Secrets[:i], func(o Secret) bool { return o.Name == sec.Name }) {
			return fmt.Errorf("secrets[%d].name: %q is listed twice", i, sec.Name)
		}
		if len(sec.Description) > maxSecretDescription {
			return fmt.Errorf("secrets[%d].description: longer than %d bytes", i, maxSecretDescription)
		}
	}
	if err := m.validateVariables(); err != nil {
		return err
	}
	if err := m.validateBackup(); err != nil {
		return err
	}
	if err := m.validateNixpkgs(); err != nil {
		return err
	}
	if !ValidName(m.Name) {
		return fmt.Errorf("name: %q must be 1-12 characters of a-z, 0-9 and '-', starting with a letter", m.Name)
	}
	seenHost := map[string]bool{}
	for i, r := range m.Routes {
		f := fmt.Sprintf("routes[%d]", i)
		label := m.Version >= 8 && IsLabel(r.Host)
		if !label && (!hostRE.MatchString(r.Host) || len(r.Host) > 253) {
			if m.Version >= 8 {
				return fmt.Errorf("%s.host: %q is not a lower-case label such as \"photos\" or a hostname", f, r.Host)
			}
			return fmt.Errorf("%s.host: %q is not a lower-case hostname", f, r.Host)
		}
		if seenHost[r.Host] {
			return fmt.Errorf("%s.host: %q is listed twice", f, r.Host)
		}
		seenHost[r.Host] = true
		if err := validPort(r.Port); err != nil {
			return fmt.Errorf("%s.port: %w", f, err)
		}
		if r.MaxBody != "" && !maxBodyRE.MatchString(r.MaxBody) {
			return fmt.Errorf("%s.max_body: %q must look like 500MB or 50GB", f, r.MaxBody)
		}
	}
	seenMount := map[string]bool{}
	for i, s := range m.Storage {
		f := fmt.Sprintf("storage[%d]", i)
		if err := validHostPath(s.HostPath); err != nil {
			return fmt.Errorf("%s.host_path: %w", f, err)
		}
		if err := validMount(s.Mount); err != nil {
			return fmt.Errorf("%s.mount: %w", f, err)
		}
		if seenMount[s.Mount] {
			return fmt.Errorf("%s.mount: %q is used twice", f, s.Mount)
		}
		seenMount[s.Mount] = true
	}
	for i, e := range m.Egress {
		f := fmt.Sprintf("egress[%d]", i)
		if n, ok := varRef(e.To); ok && m.Version >= 8 {
			if err := m.addressVar(n); err != nil {
				return fmt.Errorf("%s.to: %w", f, err)
			}
		} else if e.To != EgressHost {
			p, err := netip.ParsePrefix(e.To)
			if err != nil {
				return fmt.Errorf("%s.to: %q must be \"host\" or a CIDR such as 10.0.0.5/32", f, e.To)
			}
			if p.Bits() == 0 {
				return fmt.Errorf("%s.to: %q covers every address", f, e.To)
			}
		}
		if err := validPort(e.Port); err != nil {
			return fmt.Errorf("%s.port: %w", f, err)
		}
		if e.Proto != "tcp" && e.Proto != "udp" {
			return fmt.Errorf("%s.proto: %q must be tcp or udp", f, e.Proto)
		}
	}
	type portKey struct {
		port  int
		proto string
	}
	seenPort := map[portKey]bool{}
	for i, p := range m.Ports {
		f := fmt.Sprintf("ports[%d]", i)
		if err := validPort(p.Port); err != nil {
			return fmt.Errorf("%s.port: %w", f, err)
		}
		if portDenied[p.Port] {
			return fmt.Errorf("%s.port: %d belongs to the host", f, p.Port)
		}
		if err := validPort(p.To); err != nil {
			return fmt.Errorf("%s.to: %w", f, err)
		}
		if p.Proto != "tcp" && p.Proto != "udp" {
			return fmt.Errorf("%s.proto: %q must be tcp or udp", f, p.Proto)
		}
		k := portKey{p.Port, p.Proto}
		if seenPort[k] {
			return fmt.Errorf("%s: %d/%s is listed twice", f, p.Port, p.Proto)
		}
		seenPort[k] = true
		if len(p.From) == 0 {
			return fmt.Errorf("%s.from: name the clients, such as 192.168.1.0/24", f)
		}
		for j, c := range p.From {
			if m.Version >= 8 && c == FromLAN {
				continue
			}
			if n, ok := varRef(c); ok && m.Version >= 8 {
				if err := m.addressVar(n); err != nil {
					return fmt.Errorf("%s.from[%d]: %w", f, j, err)
				}
				continue
			}
			if slices.Contains(p.From[:j], c) {
				return fmt.Errorf("%s.from[%d]: %q is listed twice", f, j, c)
			}
			pr, err := netip.ParsePrefix(c)
			if err != nil || !pr.Addr().Is4() {
				return fmt.Errorf("%s.from[%d]: %q must be an IPv4 CIDR such as 192.168.1.0/24", f, j, c)
			}
			if pr.Bits() == 0 {
				return fmt.Errorf("%s.from[%d]: %q covers every address", f, j, c)
			}
		}
	}
	for i, u := range m.Health.Units {
		if !unitRE.MatchString(u) {
			return fmt.Errorf("health.units[%d]: %q is not a .service unit name", i, u)
		}
	}
	if err := hostUnitsClash(m.Name, m); err != nil {
		return err
	}
	if h := m.Health.HTTP; h != nil {
		if err := validPort(h.Port); err != nil {
			return fmt.Errorf("health.http.port: %w", err)
		}
		if !strings.HasPrefix(h.Path, "/") || strings.ContainsAny(h.Path, " \t\r\n") {
			return fmt.Errorf("health.http.path: %q must start with / and contain no whitespace", h.Path)
		}
	}
	return nil
}

// validateVariables checks variables, and that version 5 declares secrets
// and host binds only as variables.
func (m Manifest) validateVariables() error {
	if m.Version < 5 && len(m.Variables) > 0 {
		return errors.New("version: variables need version 5")
	}
	if m.Version >= 5 && len(m.Secrets) > 0 {
		return errors.New("secrets: version 5 declares a secret as a variable of kind \"secret\"")
	}
	if m.Version >= 5 && len(m.Storage) > 0 {
		return errors.New("storage: version 5 declares a host directory as a variable of kind \"path\"")
	}
	for i, v := range m.Variables {
		f := fmt.Sprintf("variables[%d]", i)
		if !ValidVariableName(v.Name) {
			return fmt.Errorf("%s.name: %q must be 1-64 characters of A-Z, 0-9 and '_', starting with a letter", f, v.Name)
		}
		if slices.ContainsFunc(m.Variables[:i], func(o Variable) bool { return o.Name == v.Name }) {
			return fmt.Errorf("%s.name: %q is listed twice", f, v.Name)
		}
		if len(v.Description) > maxSecretDescription {
			return fmt.Errorf("%s.description: longer than %d bytes", f, maxSecretDescription)
		}
		switch v.Kind {
		case VarEnv, VarSecret, VarPath:
		case VarAddress:
			if m.Version < 8 {
				return fmt.Errorf("version: %s.kind %q needs version 8", f, v.Kind)
			}
			if v.Default != nil {
				return fmt.Errorf("%s.default: an address is the operator's; it has no default", f)
			}
		default:
			return fmt.Errorf("%s.kind: %q must be %q, %q, %q or %q", f, v.Kind, VarEnv, VarSecret, VarPath, VarAddress)
		}
		if m.Version >= 8 && IsBuiltin(v.Name) {
			return fmt.Errorf("%s.name: %s is filled in by the host", f, v.Name)
		}
		if v.Owner != "" || v.Group != "" {
			switch {
			case m.Version < 8:
				return fmt.Errorf("version: %s.owner and group need version 8", f)
			case v.Kind != VarPath:
				return fmt.Errorf("%s: owner and group are only for a path", f)
			case v.Owner != "" && !accountRE.MatchString(v.Owner):
				return fmt.Errorf("%s.owner: %q is not a user name", f, v.Owner)
			case v.Group != "" && !accountRE.MatchString(v.Group):
				return fmt.Errorf("%s.group: %q is not a group name", f, v.Group)
			case v.Owner == "root" || v.Group == "root":
				return fmt.Errorf("%s: root cannot take a host directory's ids", f)
			}
		}
		if v.Kind != VarPath && (v.Mount != "" || v.ReadOnly) {
			return fmt.Errorf("%s: mount and read_only are only for a path", f)
		}
		if err := m.validateEnvShape(f, v); err != nil {
			return err
		}
		if d := v.Default; d != nil {
			switch {
			case v.Kind == VarSecret:
				return fmt.Errorf("%s.default: a secret has no default", f)
			case *d == "":
				return fmt.Errorf("%s.default: empty; leave it out for no default", f)
			case len(*d) > maxVarValue:
				return fmt.Errorf("%s.default: longer than %d bytes", f, maxVarValue)
			case v.Kind == VarPath:
				if err := validHostPath(*d); err != nil {
					return fmt.Errorf("%s.default: %w", f, err)
				}
			}
		}
		if v.Kind == VarPath {
			if v.Mount == "" {
				return fmt.Errorf("%s.mount: a path needs the mount point inside the stack", f)
			}
			if err := validMount(v.Mount); err != nil {
				return fmt.Errorf("%s.mount: %w", f, err)
			}
			if slices.ContainsFunc(m.Variables[:i], func(o Variable) bool { return o.Kind == VarPath && o.Mount == v.Mount }) {
				return fmt.Errorf("%s.mount: %q is used twice", f, v.Mount)
			}
		}
	}
	return nil
}

// addressVar checks that name is an address variable m declares.
func (m Manifest) addressVar(name string) error {
	if v, ok := m.Variable(name); !ok || v.Kind != VarAddress {
		return fmt.Errorf("$%s is not an address variable this manifest declares", name)
	}
	return nil
}

// validateEnvShape checks a variable's version 7 fields: an env variable has
// no default (the stack's flake holds it) and may have a type and allowed
// values.
func (m Manifest) validateEnvShape(f string, v Variable) error {
	if m.Version < 7 {
		if v.Type != "" || len(v.Enum) > 0 {
			return fmt.Errorf("version: %s.type and enum need version 7", f)
		}
		return nil
	}
	if v.Kind != VarEnv && (v.Type != "" || len(v.Enum) > 0) {
		return fmt.Errorf("%s: type and enum are only for an env variable", f)
	}
	if v.Kind == VarEnv && v.Default != nil {
		return fmt.Errorf("%s.default: from version 7 an env variable's default is the value the stack's flake sets", f)
	}
	switch v.Type {
	case "", TypeString, TypeNumber, TypeBoolean:
	default:
		return fmt.Errorf("%s.type: %q must be %q, %q or %q", f, v.Type, TypeString, TypeNumber, TypeBoolean)
	}
	if len(v.Enum) > maxEnum {
		return fmt.Errorf("%s.enum: more than %d values", f, maxEnum)
	}
	for i, e := range v.Enum {
		if e == "" || len(e) > maxVarValue {
			return fmt.Errorf("%s.enum[%d]: must be 1-%d bytes", f, i, maxVarValue)
		}
		if slices.Contains(v.Enum[:i], e) {
			return fmt.Errorf("%s.enum[%d]: %q is listed twice", f, i, e)
		}
	}
	return nil
}

// A nixpkgs channel a version 7 manifest may name.
var channelRE = regexp.MustCompile(`^nixos-(unstable|[0-9]{2}\.[0-9]{2})$`)

// commitRE is a full git commit.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidNixpkgs reports whether s names a nixpkgs: a channel or a commit.
func ValidNixpkgs(s string) bool { return channelRE.MatchString(s) || commitRE.MatchString(s) }

// validateNixpkgs checks the version 7 nixpkgs field.
func (m Manifest) validateNixpkgs() error {
	if m.Version < 7 {
		if m.Nixpkgs != "" {
			return errors.New("version: nixpkgs needs version 7")
		}
		return nil
	}
	if !ValidNixpkgs(m.Nixpkgs) {
		return fmt.Errorf("nixpkgs: %q must be nixos-unstable, nixos-YY.MM or a 40-character NixOS/nixpkgs commit", m.Nixpkgs)
	}
	return nil
}

// maxBackupLabel bounds a recommended folder's label and an exclude's reason
// and pattern.
const maxBackupLabel = 300

// validateBackup checks the backup section.
func (m Manifest) validateBackup() error {
	b := m.Backup
	if b == nil {
		return nil
	}
	if m.Version < 6 {
		return errors.New("version: backup needs version 6")
	}
	if b.Prepare != "" && !unitRE.MatchString(b.Prepare) {
		return fmt.Errorf("backup.prepare: %q is not a .service unit name", b.Prepare)
	}
	for i, d := range b.Folders {
		f := fmt.Sprintf("backup.folders[%d]", i)
		if strings.TrimSpace(d.Label) == "" {
			return fmt.Errorf("%s.label: a folder needs a label", f)
		}
		if len(d.Label) > maxBackupLabel || strings.ContainsAny(d.Label, "\n\x00") {
			return fmt.Errorf("%s.label: must be one line of at most %d bytes", f, maxBackupLabel)
		}
		switch {
		case d.Path != "" && d.Variable != "":
			return fmt.Errorf("%s: give path or variable, not both", f)
		case d.Path == "" && d.Variable == "":
			return fmt.Errorf("%s: needs a path inside the stack or the variable of a host path", f)
		case d.Path != "":
			if err := cleanAbs(d.Path); err != nil {
				return fmt.Errorf("%s.path: %w", f, err)
			}
			if d.Path == "/" {
				return fmt.Errorf("%s.path: \"/\" is the whole stack; name the directory that holds the data", f)
			}
		default:
			if v, ok := m.Variable(d.Variable); !ok || v.Kind != VarPath {
				return fmt.Errorf("%s.variable: %q is not a path variable this manifest declares", f, d.Variable)
			}
		}
		if slices.ContainsFunc(b.Folders[:i], func(o BackupDir) bool { return o.Key() == d.Key() }) {
			return fmt.Errorf("%s: %q is listed twice", f, d.Key())
		}
		for j, x := range d.Exclude {
			xf := fmt.Sprintf("%s.exclude[%d]", f, j)
			if err := ValidBackupPattern(x.Pattern); err != nil {
				return fmt.Errorf("%s.pattern: %w", xf, err)
			}
			if strings.TrimSpace(x.Reason) == "" {
				return fmt.Errorf("%s.reason: say why %q is left out", xf, x.Pattern)
			}
			if len(x.Reason) > maxBackupLabel || strings.ContainsAny(x.Reason, "\n\x00") {
				return fmt.Errorf("%s.reason: must be one line of at most %d bytes", xf, maxBackupLabel)
			}
		}
	}
	return nil
}

// ValidBackupPattern checks a pattern that leaves files out of a backed-up
// folder. It is anchored, starting with "/" (the folder itself) or "**/"
// (any depth), and uses only literal characters, "*", "**" and "?", so one
// pattern means the same to rclone and to restic.
func ValidBackupPattern(p string) error {
	rest, ok := strings.CutPrefix(p, "**/")
	if !ok {
		rest, ok = strings.CutPrefix(p, "/")
	}
	if !ok {
		return fmt.Errorf("%q must start with / (the folder itself) or **/ (any depth)", p)
	}
	if rest == "" {
		return fmt.Errorf("%q names nothing", p)
	}
	if len(p) > maxBackupLabel {
		return fmt.Errorf("pattern longer than %d bytes", maxBackupLabel)
	}
	if strings.Contains(p, "***") {
		return fmt.Errorf("%q: use * or **", p)
	}
	for _, r := range p {
		if strings.ContainsRune("[]{}\\", r) || r < 0x20 || r == 0x7f {
			return fmt.Errorf("%q may use only plain characters, *, ** and ?", p)
		}
	}
	return nil
}

func validPort(p int) error {
	if p < 1 || p > 65535 {
		return fmt.Errorf("%d is not a port", p)
	}
	return nil
}

// cleanAbs accepts only an absolute path already in its shortest form, so a
// path cannot hide a parent step or be written two ways.
func cleanAbs(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("%q must be absolute", p)
	}
	if path.Clean(p) != p || strings.ContainsAny(p, ":\\\n\x00") {
		return fmt.Errorf("%q must be a plain path without '.', '..', a trailing slash, ':' or '\\'", p)
	}
	return nil
}

func under(p string, roots []string) (string, bool) {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+"/") {
			return r, true
		}
	}
	return "", false
}

func validHostPath(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	// The pools are under /var, which is otherwise the host's own.
	if rest, ok := strings.CutPrefix(p, storage.MountRoot+"/"); ok {
		if !strings.Contains(rest, "/") {
			return fmt.Errorf("%q is a whole pool; name the directory the stack needs", p)
		}
		return nil
	}
	if r, ok := under(p, hostPathDenied()); ok {
		return fmt.Errorf("%q is under %s, which no stack may be given", p, r)
	}
	// A whole top-level tree such as /srv is never the narrowest path.
	if strings.Count(p, "/") < 2 {
		return fmt.Errorf("%q is a whole top-level directory; name the directory the stack needs", p)
	}
	return nil
}

func validMount(p string) error {
	if err := cleanAbs(p); err != nil {
		return err
	}
	if p == "/" {
		return errors.New("\"/\" cannot be a mount point")
	}
	if r, ok := under(p, mountDenied); ok {
		return fmt.Errorf("%q is under %s, which the stack's own system owns", p, r)
	}
	return nil
}

// ── grants ──────────────────────────────────────────────────────────────

// Grant kinds.
const (
	GrantRoute      = "route"
	GrantStorage    = "storage"
	GrantGPU        = "gpu"
	GrantEgress     = "egress"
	GrantPort       = "port"
	GrantContainers = "containers"
	GrantAPI        = "api"
)

// Grant is one thing a manifest asks of the host, in a form the operator
// reads before approving.
type Grant struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// Grants lists everything m asks for, in manifest order.
func (m Manifest) Grants() []Grant {
	out := []Grant{}
	for _, r := range m.Routes {
		out = append(out, Grant{GrantRoute, r.Host})
	}
	for _, s := range m.Storage {
		mode := "read-write"
		if s.ReadOnly {
			mode = "read-only"
		}
		out = append(out, Grant{GrantStorage, fmt.Sprintf("%s at %s, %s", s.HostPath, s.Mount, mode)})
	}
	for _, v := range m.pathVars() {
		out = append(out, Grant{GrantStorage, v.grantDetail()})
	}
	if m.GPU {
		out = append(out, Grant{GrantGPU, "/dev/dri"})
	}
	for _, e := range m.Egress {
		out = append(out, Grant{GrantEgress, fmt.Sprintf("%s port %d/%s", addrDetail(e.To), e.Port, e.Proto)})
	}
	for _, p := range m.Ports {
		out = append(out, Grant{GrantPort, p.detail()})
	}
	if m.Containers {
		out = append(out, Grant{GrantContainers, "podman (system calls bpf, add_key, keyctl)"})
	}
	for _, a := range m.API {
		out = append(out, Grant{GrantAPI, a})
	}
	return out
}

// grantDetail describes a path variable as a storage grant.
func (v Variable) grantDetail() string {
	mode := "read-write"
	if v.ReadOnly {
		mode = "read-only"
	}
	if v.Default != nil {
		return fmt.Sprintf("%s: %s (default) at %s, %s", v.Name, *v.Default, v.Mount, mode)
	}
	return fmt.Sprintf("%s at %s, %s, host path chosen by you", v.Name, v.Mount, mode)
}

func (p Port) detail() string {
	from := make([]string, len(p.From))
	for i, c := range p.From {
		from[i] = addrDetail(c)
	}
	return fmt.Sprintf("host port %d/%s from %s to stack port %d", p.Port, p.Proto, strings.Join(from, ", "), p.To)
}

// addrDetail is an egress destination or port client as the operator reads
// it in a grant.
func addrDetail(s string) string {
	if n, ok := varRef(s); ok {
		return n + " (the addresses you give)"
	}
	if s == FromLAN {
		return "the host's LAN"
	}
	return s
}

// NewGrants returns what next asks for that approved does not already
// allow. An empty result means next can replace approved without asking.
// Dropping a grant, and going from read-write to read-only on the same
// paths, need no approval. A path variable is new unless approved already
// binds its mount: as a path variable, whose host path the operator chose,
// or as storage from the path's default, so moving storage to a path
// variable with the same bind asks nothing.
func NewGrants(approved, next Manifest) []Grant {
	routes := map[string]bool{}
	for _, r := range approved.Routes {
		routes[r.Host] = true
	}
	type bind struct{ host, mount string }
	binds := map[bind]bool{} // value: writable
	for _, s := range approved.Storage {
		binds[bind{s.HostPath, s.Mount}] = !s.ReadOnly
	}
	paths := map[string]bool{} // mount of an approved path variable: writable
	for _, v := range approved.pathVars() {
		paths[v.Mount] = !v.ReadOnly
	}
	egress := map[Egress]bool{}
	for _, e := range approved.Egress {
		egress[e] = true
	}

	out := []Grant{}
	all := next.Grants()
	i := 0
	take := func() Grant { g := all[i]; i++; return g }
	for _, r := range next.Routes {
		if g := take(); !routes[r.Host] {
			out = append(out, g)
		}
	}
	for _, s := range next.Storage {
		g := take()
		writable, ok := binds[bind{s.HostPath, s.Mount}]
		if !ok || (!s.ReadOnly && !writable) {
			out = append(out, g)
		}
	}
	for _, v := range next.pathVars() {
		g := take()
		writable, ok := paths[v.Mount]
		if !ok && v.Default != nil {
			writable, ok = binds[bind{*v.Default, v.Mount}]
		}
		if !ok || (!v.ReadOnly && !writable) {
			out = append(out, g)
		}
	}
	if next.GPU {
		if g := take(); !approved.GPU {
			out = append(out, g)
		}
	}
	for _, e := range next.Egress {
		if g := take(); !egress[e] {
			out = append(out, g)
		}
	}
	for _, p := range next.Ports {
		if g := take(); !portAllowed(approved.Ports, p) {
			out = append(out, g)
		}
	}
	if next.Containers {
		if g := take(); !approved.Containers {
			out = append(out, g)
		}
	}
	for _, a := range next.API {
		if g := take(); !approved.HasAPI(a) {
			out = append(out, g)
		}
	}
	return out
}

// portAllowed reports whether an approved port forward already covers p:
// the same host port, protocol and target, and every client range of p
// inside one the operator approved.
func portAllowed(approved []Port, p Port) bool {
	for _, a := range approved {
		if a.Port != p.Port || a.Proto != p.Proto || a.To != p.To {
			continue
		}
		covered := true
		for _, c := range p.From {
			if !prefixCovered(a.From, c) {
				covered = false
				break
			}
		}
		return covered
	}
	return false
}

func prefixCovered(ranges []string, c string) bool {
	if _, ok := varRef(c); ok || c == FromLAN {
		return slices.Contains(ranges, c)
	}
	cp, err := netip.ParsePrefix(c)
	if err != nil {
		return false
	}
	for _, r := range ranges {
		rp, err := netip.ParsePrefix(r)
		if err == nil && rp.Bits() <= cp.Bits() && rp.Contains(cp.Addr()) {
			return true
		}
	}
	return false
}
