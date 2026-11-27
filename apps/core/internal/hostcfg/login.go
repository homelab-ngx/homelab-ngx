package hostcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// ── login links ──────────────────────────────────────────────────────────
//
// Tailscale and the Cloudflare tunnel are logged in from the dashboard: the
// daemon runs the tool's own login, which prints a link and waits, and the
// operator opens the link on any device and approves the host there.

const (
	// loginTimeout is how long a login link stays open.
	loginTimeout = 10 * time.Minute
	// loginWait is how long a start waits for the tool to print its link.
	loginWait = 15 * time.Second
)

// LoginState is where a login started from the dashboard is.
type LoginState string

const (
	LoginIdle    LoginState = "idle"
	LoginWaiting LoginState = "waiting"
	LoginDone    LoginState = "done"
	LoginFailed  LoginState = "failed"
)

// Login is a login the daemon runs: its link while it waits, and how it ended.
type Login struct {
	State     LoginState `json:"state" enum:"idle,waiting,done,failed" doc:"idle: none started since the daemon started. waiting: open url to approve the host. done or failed: how the last one ended."`
	URL       string     `json:"url,omitempty" doc:"The link to open and approve the host at, while waiting."`
	ExpiresAt *time.Time `json:"expires_at,omitempty" doc:"When the login link expires, while waiting."`
	Error     string     `json:"error,omitempty" doc:"Why the last login failed."`
}

// loginURL finds the link a login prints.
var loginURL = regexp.MustCompile(`https://\S+`)

// loginJob runs one login at a time in the background.
type loginJob struct {
	mu     sync.Mutex
	l      Login
	cancel context.CancelFunc
	gen    uint64
}

func (j *loginJob) get() Login {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.l.State == "" {
		return Login{State: LoginIdle}
	}
	return j.l
}

// cancelWaiting aborts an in-flight waiting login so a fresh attempt can start.
func (j *loginJob) cancelWaiting() {
	j.mu.Lock()
	cancel := j.cancel
	j.cancel = nil
	if j.l.State == LoginWaiting {
		j.l = Login{State: LoginIdle}
	}
	j.gen++
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelJob aborts a waiting login and returns the job to idle.
func (j *loginJob) cancelJob() Login {
	j.mu.Lock()
	cancel := j.cancel
	j.cancel = nil
	j.gen++
	j.l = Login{State: LoginIdle}
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return Login{State: LoginIdle}
}

// start runs fn unless a login is waiting already, and returns once fn has
// seen its link, has ended, or after loginWait. fn gets a context that ends
// with the link's lifetime and passes every line of output to line.
func (j *loginJob) start(fn func(ctx context.Context, line func(stream, s string)) error) Login {
	j.mu.Lock()
	if j.l.State == LoginWaiting {
		defer j.mu.Unlock()
		return j.l
	}
	exp := time.Now().Add(loginTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	j.gen++
	myGen := j.gen
	j.cancel = cancel
	j.l = Login{State: LoginWaiting, ExpiresAt: &exp}
	j.mu.Unlock()

	seen := make(chan struct{})
	var once sync.Once
	signal := func() { once.Do(func() { close(seen) }) }
	line := func(_, s string) {
		u := loginURL.FindString(s)
		if u == "" {
			return
		}
		j.mu.Lock()
		if j.gen == myGen && j.l.URL == "" {
			j.l.URL = u
		}
		j.mu.Unlock()
		signal()
	}
	go func() {
		defer cancel()
		err := fn(ctx, line)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("the link expired before the host was approved; start again")
		}
		j.mu.Lock()
		if j.gen == myGen {
			j.cancel = nil
			if err != nil {
				j.l = Login{State: LoginFailed, Error: commandError(err)}
			} else {
				j.l = Login{State: LoginDone}
			}
		}
		j.mu.Unlock()
		signal()
	}()
	select {
	case <-seen:
	case <-time.After(loginWait):
	}
	return j.get()
}

// commandError is a failed command's own message, without the runner's
// wrapping.
func commandError(err error) string {
	var ce *cmdrunner.Error
	if errors.As(err, &ce) {
		if msg := strings.TrimSpace(ce.Result.Stderr); msg != "" {
			lines := strings.Split(msg, "\n")
			return strings.TrimSpace(lines[len(lines)-1])
		}
	}
	return err.Error()
}

// ── Tailscale ────────────────────────────────────────────────────────────

const (
	tailscaleDefaultControlURL = "https://controlplane.tailscale.com"
	tailscaleAdminURL          = "https://login.tailscale.com/admin/machines"
)

// TailscaleState is where the host is on its tailnet.
type TailscaleState string

const (
	TailscaleOff           TailscaleState = "off"
	TailscaleNeedsLogin    TailscaleState = "needs-login"
	TailscaleNeedsApproval TailscaleState = "needs-approval"
	TailscaleStopped       TailscaleState = "stopped"
	TailscaleExpired       TailscaleState = "expired"
	TailscaleStarting      TailscaleState = "starting"
	TailscaleRunning       TailscaleState = "running"
)

// TailscaleExitNode is whether this host acts as an exit node.
type TailscaleExitNode string

const (
	TailscaleExitOff      TailscaleExitNode = "off"
	TailscaleExitPending  TailscaleExitNode = "pending"
	TailscaleExitApproved TailscaleExitNode = "approved"
)

// TailscaleRoute is one offered subnet and whether the tailnet approved it.
type TailscaleRoute struct {
	Prefix   string `json:"prefix" doc:"Subnet prefix in CIDR notation, such as 192.168.1.0/24."`
	Approved bool   `json:"approved" doc:"Whether the tailnet admin console approved this route."`
}

// TailscalePeer is another device on the tailnet.
type TailscalePeer struct {
	Name     string     `json:"name"`
	DNSName  string     `json:"dns_name,omitempty"`
	OS       string     `json:"os,omitempty"`
	IPs      []string   `json:"ips"`
	Online   bool       `json:"online"`
	Direct   bool       `json:"direct"`
	Relay    string     `json:"relay,omitempty"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
}

// TailscalePeers lists devices on the host's tailnet.
type TailscalePeers struct {
	Peers []TailscalePeer `json:"peers"`
}

// TailscaleLoginInput configures a login attempt. An empty body starts the
// standard browser link flow.
type TailscaleLoginInput struct {
	AuthKey     string  `json:"auth_key,omitempty" maxLength:"512" doc:"Optional node authorization key. Passed via a 0600 temp file and never stored or logged."`
	LoginServer *string `json:"login_server,omitempty" maxLength:"256" doc:"Optional custom control server URL (https://...). Empty uses Tailscale. Absent keeps the stored value."`
	ForceReauth bool    `json:"force_reauth,omitempty" doc:"Force re-authentication when already logged in or switching control servers."`
}

// TailscaleStatus is what tailscaled reports about the host.
type TailscaleStatus struct {
	State       TailscaleState    `json:"state" enum:"off,needs-login,needs-approval,stopped,expired,starting,running" doc:"off: tailscaled is not running. needs-login: open auth_url to add the host to a tailnet. needs-approval: an admin of the tailnet approves the host in its admin console. stopped: logged in previously but stopped. expired: node key expired. running: on the tailnet."`
	AuthURL     string            `json:"auth_url,omitempty" doc:"The link that adds the host to a tailnet, while it needs a login."`
	Tailnet     string            `json:"tailnet,omitempty" doc:"The tailnet's name."`
	DNSName     string            `json:"dns_name,omitempty" doc:"The host's name on the tailnet, such as homelab.tail1234.ts.net."`
	IPs         []string          `json:"ips" doc:"The host's addresses on the tailnet."`
	Version     string            `json:"version,omitempty" doc:"The running Tailscale version."`
	KeyExpiry   *time.Time        `json:"key_expiry,omitempty" doc:"When the node key expires, if key expiry is enabled."`
	Tags        []string          `json:"tags" doc:"ACL tags assigned to this node."`
	User        string            `json:"user,omitempty" doc:"The account or login name the node authenticated as."`
	Relay       string            `json:"relay,omitempty" doc:"Preferred DERP relay code for this host."`
	Health      []string          `json:"health" doc:"Health warnings reported by tailscaled."`
	Routes      []TailscaleRoute  `json:"routes" doc:"Offered subnets with approval state from Self.AllowedIPs."`
	ExitNode    TailscaleExitNode `json:"exit_node" enum:"off,pending,approved" doc:"off, pending, or approved exit-node status."`
	ControlURL  string            `json:"control_url,omitempty" doc:"The control server URL in use."`
	AdminURL    string            `json:"admin_url,omitempty" doc:"Tailscale admin console machines page, omitted for custom control servers."`
	OnlinePeers int               `json:"online_peers" doc:"Number of online peers on the tailnet."`
	TotalPeers  int               `json:"total_peers" doc:"Total peers visible on the tailnet."`
}

func emptyTailscaleStatus() TailscaleStatus {
	return TailscaleStatus{
		State:    TailscaleOff,
		IPs:      []string{},
		Tags:     []string{},
		Health:   []string{},
		Routes:   []TailscaleRoute{},
		ExitNode: TailscaleExitOff,
	}
}

func normalizeControlURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return tailscaleDefaultControlURL
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(s, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return strings.TrimRight(u.String(), "/")
}

func isTailscaleControlServer(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return true
	}
	u, err := url.Parse(normalizeControlURL(raw))
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "controlplane.tailscale.com" || h == "login.tailscale.com"
}

func sameControlServer(a, b string) bool {
	return normalizeControlURL(a) == normalizeControlURL(b)
}

// tailscalePrefArgs returns the desired tailscale preference flags, always
// pinning accept-routes=false so remote subnet routers never hijack LAN traffic.
func tailscalePrefArgs(ts Tailscale, hostname string) []string {
	dns := "false"
	if ts.UseDNS() {
		dns = "true"
	}
	exit := "false"
	if ts.ExitNode {
		exit = "true"
	}
	ssh := "false"
	if ts.SSH {
		ssh = "true"
	}
	args := []string{
		"--accept-routes=false",
		"--accept-dns=" + dns,
		"--advertise-routes=" + strings.Join(ts.AdvertiseRoutes, ","),
		"--advertise-exit-node=" + exit,
		"--ssh=" + ssh,
	}
	if hostname != "" {
		args = append(args, "--hostname="+hostname)
	}
	return args
}

// readTailscale asks tailscaled for its status; a tailscaled that does not
// answer is off.
func readTailscale(ctx context.Context, r cmdrunner.Runner, cfg Tailscale, now time.Time) TailscaleStatus {
	if r == nil {
		return emptyTailscaleStatus()
	}
	res, err := r.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: []string{"status", "--json"}, ExpectExit: []int{1}})
	if err != nil && res.Stdout == "" {
		return emptyTailscaleStatus()
	}
	return parseTailscale(res.Stdout, cfg, now)
}

type rawTailscaleStatus struct {
	BackendState   string
	AuthURL        string
	Version        string
	Health         []string
	CurrentTailnet *struct {
		Name       string
		ControlURL string
	}
	Self *struct {
		DNSName        string
		TailscaleIPs   []string
		AllowedIPs     []string
		PrimaryRoutes  []string
		KeyExpiry      *time.Time
		Expired        bool
		Tags           []string
		Relay          string
		UserID         int64
		ExitNodeOption bool
	}
	User map[string]struct {
		LoginName   string
		DisplayName string
	}
	Peer map[string]struct {
		HostName     string
		DNSName      string
		OS           string
		TailscaleIPs []string
		Online       bool
		CurAddr      string
		Relay        string
		LastSeen     *time.Time
	}
}

// parseTailscale reads tailscale status --json and enriches routes and exit
// node status from cfg.
func parseTailscale(out string, cfg Tailscale, now time.Time) TailscaleStatus {
	st := emptyTailscaleStatus()
	var raw rawTailscaleStatus
	if json.Unmarshal([]byte(out), &raw) != nil {
		return st
	}
	switch raw.BackendState {
	case "Running":
		st.State = TailscaleRunning
	case "NeedsLogin":
		st.State = TailscaleNeedsLogin
	case "Stopped":
		st.State = TailscaleStopped
	case "NeedsMachineAuth":
		st.State = TailscaleNeedsApproval
	case "NoState", "Starting":
		st.State = TailscaleStarting
	default:
		return st
	}
	st.AuthURL = raw.AuthURL
	st.Version = strings.TrimSpace(raw.Version)
	for _, h := range raw.Health {
		if h = strings.TrimSpace(h); h != "" {
			st.Health = append(st.Health, h)
		}
	}
	if raw.CurrentTailnet != nil {
		st.Tailnet = raw.CurrentTailnet.Name
	}

	if raw.Self != nil && raw.Self.KeyExpiry != nil && !raw.Self.KeyExpiry.IsZero() {
		t := *raw.Self.KeyExpiry
		st.KeyExpiry = &t
	}
	if raw.Self != nil && (st.State == TailscaleRunning || st.State == TailscaleNeedsLogin) {
		if raw.Self.Expired || (st.KeyExpiry != nil && !st.KeyExpiry.After(now)) {
			st.State = TailscaleExpired
		}
	}

	allowed := map[netip.Prefix]bool{}
	if raw.Self != nil {
		for _, list := range [][]string{raw.Self.AllowedIPs, raw.Self.PrimaryRoutes} {
			for _, s := range list {
				if p, err := netip.ParsePrefix(s); err == nil {
					allowed[p.Masked()] = true
				}
			}
		}
	}

	hasIdentity := raw.Self != nil && (st.State == TailscaleRunning || st.State == TailscaleNeedsApproval || st.State == TailscaleStopped || st.State == TailscaleExpired)
	if hasIdentity {
		st.DNSName = strings.TrimSuffix(raw.Self.DNSName, ".")
		if raw.Self.TailscaleIPs != nil {
			st.IPs = slices.Clone(raw.Self.TailscaleIPs)
		}
		st.Relay = raw.Self.Relay
		for _, tag := range raw.Self.Tags {
			if tag = strings.TrimSpace(tag); tag != "" {
				st.Tags = append(st.Tags, tag)
			}
		}
		if raw.Self.UserID != 0 {
			if u, ok := raw.User[strconv.FormatInt(raw.Self.UserID, 10)]; ok {
				name := u.LoginName
				if name == "" {
					name = u.DisplayName
				}
				if !(len(st.Tags) > 0 && name == "tagged-devices") {
					st.User = name
				}
			}
		}
	}

	if st.State != TailscaleOff && st.State != TailscaleNeedsLogin && st.State != TailscaleStarting {
		seen := map[netip.Prefix]bool{}
		for _, r := range cfg.AdvertiseRoutes {
			if p, err := netip.ParsePrefix(r); err == nil {
				m := p.Masked()
				if !seen[m] {
					seen[m] = true
					st.Routes = append(st.Routes, TailscaleRoute{
						Prefix:   m.String(),
						Approved: st.State == TailscaleRunning && allowed[m],
					})
				}
			}
		}
		if st.State == TailscaleRunning && raw.Self != nil {
			var extra []string
			for _, s := range raw.Self.AllowedIPs {
				if p, err := netip.ParsePrefix(s); err == nil {
					m := p.Masked()
					if m.Bits() > 0 && m.Bits() < m.Addr().BitLen() && !seen[m] {
						seen[m] = true
						extra = append(extra, m.String())
					}
				}
			}
			slices.Sort(extra)
			for _, s := range extra {
				st.Routes = append(st.Routes, TailscaleRoute{Prefix: s, Approved: true})
			}
		}
	}

	exitApproved := raw.Self != nil && (raw.Self.ExitNodeOption ||
		allowed[netip.MustParsePrefix("0.0.0.0/0")] ||
		allowed[netip.MustParsePrefix("::/0")])
	switch {
	case st.State == TailscaleRunning && exitApproved:
		st.ExitNode = TailscaleExitApproved
	case (st.State == TailscaleRunning || st.State == TailscaleNeedsApproval) && cfg.ExitNode:
		st.ExitNode = TailscaleExitPending
	default:
		st.ExitNode = TailscaleExitOff
	}

	switch {
	case cfg.LoginServer != "":
		st.ControlURL = normalizeControlURL(cfg.LoginServer)
	case raw.CurrentTailnet != nil && raw.CurrentTailnet.ControlURL != "":
		st.ControlURL = normalizeControlURL(raw.CurrentTailnet.ControlURL)
	case st.AuthURL != "":
		if u, err := url.Parse(st.AuthURL); err == nil && u.Scheme != "" && u.Host != "" && !isTailscaleControlServer(st.AuthURL) {
			st.ControlURL = strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
		} else {
			st.ControlURL = tailscaleDefaultControlURL
		}
	default:
		st.ControlURL = tailscaleDefaultControlURL
	}
	if isTailscaleControlServer(st.ControlURL) {
		st.AdminURL = tailscaleAdminURL
	}

	st.TotalPeers = len(raw.Peer)
	for _, p := range raw.Peer {
		if p.Online {
			st.OnlinePeers++
		}
	}
	return st
}

// parseTailscalePeers reads the peer map from tailscale status --json.
func parseTailscalePeers(out string) TailscalePeers {
	res := TailscalePeers{Peers: []TailscalePeer{}}
	var raw rawTailscaleStatus
	if json.Unmarshal([]byte(out), &raw) != nil || len(raw.Peer) == 0 {
		return res
	}
	for _, p := range raw.Peer {
		dnsName := strings.TrimSuffix(p.DNSName, ".")
		name, _, _ := strings.Cut(dnsName, ".")
		if name == "" {
			name = p.HostName
		}
		var seen *time.Time
		if p.LastSeen != nil && !p.LastSeen.IsZero() {
			t := *p.LastSeen
			seen = &t
		}
		res.Peers = append(res.Peers, TailscalePeer{
			Name:     name,
			DNSName:  dnsName,
			OS:       p.OS,
			IPs:      nonNil(slices.Clone(p.TailscaleIPs)),
			Online:   p.Online,
			Direct:   p.CurAddr != "",
			Relay:    p.Relay,
			LastSeen: seen,
		})
	}
	slices.SortFunc(res.Peers, func(a, b TailscalePeer) int {
		if a.Online != b.Online {
			if a.Online {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return res
}

func (s *Service) tailscaleConfig(ctx context.Context) Tailscale {
	if s.managed == ManagedNixOS {
		return s.fromNix().Tailscale
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return Tailscale{AcceptDNS: boolPtr(true)}
	}
	return snap.Tailscale
}

// TailscaleStatus reports the host on its tailnet, with the link of a login
// started here while it waits.
func (s *Service) TailscaleStatus(ctx context.Context) TailscaleStatus {
	cfg := s.tailscaleConfig(ctx)
	st := readTailscale(ctx, s.run, cfg, s.now())
	if st.State != TailscaleOff && st.AuthURL == "" {
		if l := s.tailscaleLogin.get(); l.State == LoginWaiting && l.URL != "" {
			st.AuthURL = l.URL
		}
	}
	return st
}

// TailscalePeers lists the host's tailnet peers.
func (s *Service) TailscalePeers(ctx context.Context) TailscalePeers {
	if s.run == nil {
		return TailscalePeers{Peers: []TailscalePeer{}}
	}
	res, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: []string{"status", "--json"}, ExpectExit: []int{1}})
	if err != nil && res.Stdout == "" {
		return TailscalePeers{Peers: []TailscalePeer{}}
	}
	return parseTailscalePeers(res.Stdout)
}

func redactSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

// TailscaleLogin turns Tailscale on if it is off and starts login (browser
// link by default, or auth key / custom control server when supplied).
func (s *Service) TailscaleLogin(ctx context.Context, in TailscaleLoginInput) (TailscaleStatus, error) {
	if s.managed == ManagedNixOS {
		return TailscaleStatus{}, ErrManaged
	}
	authKey := strings.TrimSpace(in.AuthKey)
	if authKey != "" && strings.ContainsFunc(authKey, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return TailscaleStatus{}, invalid("auth_key: must not contain spaces or new lines")
	}
	cur, err := s.Get(ctx)
	if err != nil {
		return TailscaleStatus{}, err
	}
	targetServer := cur.Tailscale.LoginServer
	if in.LoginServer != nil {
		targetServer, err = checkLoginServer(*in.LoginServer)
		if err != nil {
			return TailscaleStatus{}, err
		}
	}
	st := readTailscale(ctx, s.run, cur.Tailscale, s.now())
	loggedIn := st.State == TailscaleRunning || st.State == TailscaleNeedsApproval || st.State == TailscaleStopped || st.State == TailscaleExpired
	if loggedIn && !sameControlServer(targetServer, st.ControlURL) && !in.ForceReauth {
		return TailscaleStatus{}, invalid("login_server: changing the control server requires force_reauth")
	}

	updated := cur.Tailscale
	if !updated.Enabled || updated.LoginServer != targetServer {
		updated.Enabled = true
		updated.LoginServer = targetServer
		if _, err := s.SetTailscale(ctx, updated); err != nil {
			return TailscaleStatus{}, err
		}
	}

	// tailscaled was just started if it was off: wait for it to answer.
	st = readTailscale(ctx, s.run, updated, s.now())
	for deadline := time.Now().Add(10 * time.Second); st.State == TailscaleOff && time.Now().Before(deadline); st = readTailscale(ctx, s.run, updated, s.now()) {
		select {
		case <-ctx.Done():
			return TailscaleStatus{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if st.State == TailscaleRunning && !in.ForceReauth {
		return st, nil
	}

	if in.ForceReauth || authKey != "" {
		s.tailscaleLogin.cancelWaiting()
	}

	var args []string
	if in.ForceReauth {
		args = append([]string{"up", "--force-reauth", "--reset"}, tailscalePrefArgs(updated, cur.Host.Hostname)...)
		if targetServer != "" {
			args = append(args, "--login-server="+targetServer)
		} else {
			args = append(args, "--login-server="+tailscaleDefaultControlURL)
		}
	} else {
		args = []string{"login", "--accept-routes=false"}
		if targetServer != "" {
			args = append(args, "--login-server="+targetServer)
		}
	}

	var keyFile string
	if authKey != "" {
		f, err := os.CreateTemp("", "homelab-tskey-*")
		if err != nil {
			return TailscaleStatus{}, err
		}
		keyFile = f.Name()
		if err := os.Chmod(keyFile, 0o600); err != nil {
			_ = f.Close()
			_ = os.Remove(keyFile)
			return TailscaleStatus{}, err
		}
		if _, err := f.WriteString(authKey); err != nil {
			_ = f.Close()
			_ = os.Remove(keyFile)
			return TailscaleStatus{}, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(keyFile)
			return TailscaleStatus{}, err
		}
		args = append(args, "--auth-key=file:"+keyFile)
	}

	l := s.tailscaleLogin.start(func(ctx context.Context, line func(string, string)) error {
		if keyFile != "" {
			defer os.Remove(keyFile)
		}
		_, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: args, OnLine: line})
		if err == nil {
			s.Apply(context.WithoutCancel(ctx))
			return nil
		}
		return errors.New(redactSecret(commandError(err), authKey))
	})
	if l.State == LoginFailed {
		return TailscaleStatus{}, fmt.Errorf("%w: tailscale login: %s", ErrUpstream, redactSecret(l.Error, authKey))
	}
	return s.TailscaleStatus(ctx), nil
}

// TailscaleLogout takes the host off its tailnet. Tailscale stays on, ready
// for a new login.
func (s *Service) TailscaleLogout(ctx context.Context) (TailscaleStatus, error) {
	if s.managed == ManagedNixOS {
		return TailscaleStatus{}, ErrManaged
	}
	s.tailscaleLogin.cancelWaiting()
	if _, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: []string{"logout"}}); err != nil {
		return TailscaleStatus{}, fmt.Errorf("%w: tailscale logout: %s", ErrUpstream, commandError(err))
	}
	return s.TailscaleStatus(ctx), nil
}

// ── Cloudflare tunnel ────────────────────────────────────────────────────

// TunnelLogin starts cloudflared tunnel login. Once the operator approves a
// zone at its link, the daemon creates the tunnel (or takes the one of the
// same name back), keeps its credentials and Cloudflare's certificate for
// the zone, and turns the tunnel on. The certificate lets the daemon add
// each route's DNS record.
func (s *Service) TunnelLogin(ctx context.Context) (Login, error) {
	if s.managed == ManagedNixOS {
		return Login{}, ErrManaged
	}
	cur, err := s.Get(ctx)
	if err != nil {
		return Login{}, err
	}
	name := cur.Host.Hostname
	if name == "" {
		name = "homelab"
	}
	return s.tunnelLogin.start(func(ctx context.Context, line func(string, string)) error {
		return s.connectTunnel(ctx, name, line)
	}), nil
}

// TunnelLoginState reports the last tunnel login.
func (s *Service) TunnelLoginState() Login { return s.tunnelLogin.get() }

// TunnelLoginCancel aborts a waiting tunnel login and returns to idle.
func (s *Service) TunnelLoginCancel() (Login, error) {
	if s.managed == ManagedNixOS {
		return Login{}, ErrManaged
	}
	return s.tunnelLogin.cancelJob(), nil
}

func (s *Service) connectTunnel(ctx context.Context, name string, line func(string, string)) error {
	// cloudflared writes the certificate to $HOME/.cloudflared/cert.pem.
	home, err := os.MkdirTemp("", "homelab-cloudflared-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	cf := func(args ...string) error {
		_, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "cloudflared", Args: args, Env: []string{"HOME=" + home}, OnLine: line})
		return err
	}
	if err := cf("tunnel", "login"); err != nil {
		return fmt.Errorf("cloudflare login: %s", commandError(err))
	}
	certPath := filepath.Join(home, ".cloudflared", "cert.pem")
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("cloudflare login left no certificate: %w", err)
	}
	credsPath := filepath.Join(home, "credentials.json")
	err = cf("tunnel", "--origincert", certPath, "create", "--credentials-file", credsPath, name)
	if err != nil && strings.Contains(strings.ToLower(commandError(err)), "already exist") {
		// The host was connected before: take its tunnel back.
		err = cf("tunnel", "--origincert", certPath, "token", "--cred-file", credsPath, name)
	}
	if err != nil {
		return fmt.Errorf("creating tunnel %s: %s", name, commandError(err))
	}
	creds, err := os.ReadFile(credsPath)
	if err != nil {
		return err
	}
	zone := s.resolveZone(context.WithoutCancel(ctx), string(cert))
	if err := s.storeTunnel(context.WithoutCancel(ctx), string(creds), string(cert), name, zone); err != nil {
		return err
	}
	s.kickTunnelProbe(true)
	return nil
}

// storeTunnel keeps a tunnel's credentials and certificate and turns it on.
func (s *Service) storeTunnel(ctx context.Context, creds, cert, name, zone string) error {
	if err := checkTunnelCredentials(creds); err != nil {
		return err
	}
	_, err := s.change(ctx, func(rs map[string]row) error {
		var t Tunnel
		if r, ok := rs["tunnel"]; ok {
			_ = json.Unmarshal(r.value, &t)
		}
		return s.save(ctx, "tunnel", Tunnel{Enabled: true, Name: name, Zone: zone, Routes: nonNil(t.Routes)}, tunnelSecret{Credentials: strings.TrimSpace(creds), Cert: cert})
	})
	if err == nil {
		s.zoneMu.Lock()
		s.zoneCheckedFor = ""
		s.zoneMu.Unlock()
	}
	return err
}

// TunnelForget drops the tunnel's credentials and certificate and turns it
// off. The tunnel and its DNS records stay at Cloudflare.
func (s *Service) TunnelForget(ctx context.Context) (HostSettings, error) {
	st, err := s.change(ctx, func(rs map[string]row) error {
		var t Tunnel
		if r, ok := rs["tunnel"]; ok {
			_ = json.Unmarshal(r.value, &t)
		}
		return s.save(ctx, "tunnel", Tunnel{Routes: nonNil(t.Routes)}, nil)
	})
	if err == nil {
		s.clearTunnelProbe()
	}
	return st, err
}
