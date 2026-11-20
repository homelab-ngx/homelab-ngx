package hostcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	State LoginState `json:"state" enum:"idle,waiting,done,failed" doc:"idle: none started since the daemon started. waiting: open url to approve the host. done or failed: how the last one ended."`
	URL   string     `json:"url,omitempty" doc:"The link to open and approve the host at, while waiting."`
	Error string     `json:"error,omitempty" doc:"Why the last login failed."`
}

// loginURL finds the link a login prints.
var loginURL = regexp.MustCompile(`https://\S+`)

// loginJob runs one login at a time in the background.
type loginJob struct {
	mu sync.Mutex
	l  Login
}

func (j *loginJob) get() Login {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.l.State == "" {
		return Login{State: LoginIdle}
	}
	return j.l
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
	j.l = Login{State: LoginWaiting}
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
		if j.l.URL == "" {
			j.l.URL = u
		}
		j.mu.Unlock()
		signal()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
		defer cancel()
		err := fn(ctx, line)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errors.New("the link expired before the host was approved; start again")
		}
		j.mu.Lock()
		if err != nil {
			j.l = Login{State: LoginFailed, Error: commandError(err)}
		} else {
			j.l = Login{State: LoginDone}
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

// TailscaleState is where the host is on its tailnet.
type TailscaleState string

const (
	TailscaleOff           TailscaleState = "off"
	TailscaleNeedsLogin    TailscaleState = "needs-login"
	TailscaleNeedsApproval TailscaleState = "needs-approval"
	TailscaleStarting      TailscaleState = "starting"
	TailscaleRunning       TailscaleState = "running"
)

// TailscaleStatus is what tailscaled reports about the host.
type TailscaleStatus struct {
	State   TailscaleState `json:"state" enum:"off,needs-login,needs-approval,starting,running" doc:"off: tailscaled is not running. needs-login: open auth_url to add the host to a tailnet. needs-approval: an admin of the tailnet approves the host in its admin console. running: on the tailnet."`
	AuthURL string         `json:"auth_url,omitempty" doc:"The link that adds the host to a tailnet, while it needs a login."`
	Tailnet string         `json:"tailnet,omitempty" doc:"The tailnet's name."`
	DNSName string         `json:"dns_name,omitempty" doc:"The host's name on the tailnet, such as homelab.tail1234.ts.net."`
	IPs     []string       `json:"ips" doc:"The host's addresses on the tailnet."`
}

// readTailscale asks tailscaled for its status; a tailscaled that does not
// answer is off.
func readTailscale(ctx context.Context, r cmdrunner.Runner) TailscaleStatus {
	off := TailscaleStatus{State: TailscaleOff, IPs: []string{}}
	if r == nil {
		return off
	}
	res, err := r.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: []string{"status", "--json"}, ExpectExit: []int{1}})
	if err != nil && res.Stdout == "" {
		return off
	}
	return parseTailscale(res.Stdout)
}

// parseTailscale reads tailscale status --json.
func parseTailscale(out string) TailscaleStatus {
	var raw struct {
		BackendState   string
		AuthURL        string
		CurrentTailnet *struct{ Name string }
		Self           *struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	st := TailscaleStatus{State: TailscaleOff, IPs: []string{}}
	if json.Unmarshal([]byte(out), &raw) != nil {
		return st
	}
	switch raw.BackendState {
	case "Running":
		st.State = TailscaleRunning
	case "NeedsLogin", "Stopped":
		st.State = TailscaleNeedsLogin
	case "NeedsMachineAuth":
		st.State = TailscaleNeedsApproval
	case "NoState", "Starting":
		st.State = TailscaleStarting
	default:
		return st
	}
	st.AuthURL = raw.AuthURL
	if raw.CurrentTailnet != nil {
		st.Tailnet = raw.CurrentTailnet.Name
	}
	if raw.Self != nil && st.State == TailscaleRunning {
		st.DNSName = strings.TrimSuffix(raw.Self.DNSName, ".")
		if raw.Self.TailscaleIPs != nil {
			st.IPs = raw.Self.TailscaleIPs
		}
	}
	return st
}

// TailscaleStatus reports the host on its tailnet, with the link of a login
// started here while it waits.
func (s *Service) TailscaleStatus(ctx context.Context) TailscaleStatus {
	st := readTailscale(ctx, s.run)
	if st.State == TailscaleNeedsLogin && st.AuthURL == "" {
		if l := s.tailscaleLogin.get(); l.State == LoginWaiting {
			st.AuthURL = l.URL
		}
	}
	return st
}

// TailscaleLogin turns Tailscale on if it is off and starts tailscale login,
// which waits for the operator to open its link. Once the host is in, the
// settings are applied again, so the tailnet gets the offered subnets.
func (s *Service) TailscaleLogin(ctx context.Context) (TailscaleStatus, error) {
	if s.managed == ManagedNixOS {
		return TailscaleStatus{}, ErrManaged
	}
	cur, err := s.Get(ctx)
	if err != nil {
		return TailscaleStatus{}, err
	}
	if !cur.Tailscale.Enabled {
		on := cur.Tailscale
		on.Enabled = true
		if _, err := s.SetTailscale(ctx, on); err != nil {
			return TailscaleStatus{}, err
		}
	}
	// tailscaled was just started if it was off: wait for it to answer.
	st := readTailscale(ctx, s.run)
	for deadline := time.Now().Add(10 * time.Second); st.State == TailscaleOff && time.Now().Before(deadline); st = readTailscale(ctx, s.run) {
		select {
		case <-ctx.Done():
			return TailscaleStatus{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if st.State == TailscaleRunning {
		return st, nil
	}
	l := s.tailscaleLogin.start(func(ctx context.Context, line func(string, string)) error {
		_, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: []string{"login"}, OnLine: line})
		if err == nil {
			s.Apply(context.WithoutCancel(ctx))
		}
		return err
	})
	if l.State == LoginFailed {
		return TailscaleStatus{}, fmt.Errorf("%w: tailscale login: %s", ErrCommand, l.Error)
	}
	return s.TailscaleStatus(ctx), nil
}

// TailscaleLogout takes the host off its tailnet. Tailscale stays on, ready
// for a new login.
func (s *Service) TailscaleLogout(ctx context.Context) (TailscaleStatus, error) {
	if s.managed == ManagedNixOS {
		return TailscaleStatus{}, ErrManaged
	}
	if _, err := s.run.Run(ctx, cmdrunner.Cmd{Module: "hostcfg", Name: "tailscale", Args: []string{"logout"}}); err != nil {
		return TailscaleStatus{}, fmt.Errorf("%w: tailscale logout: %s", ErrCommand, commandError(err))
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
	return s.storeTunnel(context.WithoutCancel(ctx), string(creds), string(cert))
}

// storeTunnel keeps a tunnel's credentials and certificate and turns it on.
func (s *Service) storeTunnel(ctx context.Context, creds, cert string) error {
	if err := checkTunnelCredentials(creds); err != nil {
		return err
	}
	_, err := s.change(ctx, func(rs map[string]row) error {
		var t Tunnel
		if r, ok := rs["tunnel"]; ok {
			_ = json.Unmarshal(r.value, &t)
		}
		return s.save(ctx, "tunnel", Tunnel{Enabled: true, Routes: nonNil(t.Routes)}, tunnelSecret{Credentials: strings.TrimSpace(creds), Cert: cert})
	})
	return err
}

// TunnelForget drops the tunnel's credentials and certificate and turns it
// off. The tunnel and its DNS records stay at Cloudflare.
func (s *Service) TunnelForget(ctx context.Context) (HostSettings, error) {
	return s.change(ctx, func(rs map[string]row) error {
		var t Tunnel
		if r, ok := rs["tunnel"]; ok {
			_ = json.Unmarshal(r.value, &t)
		}
		return s.save(ctx, "tunnel", Tunnel{Routes: nonNil(t.Routes)}, nil)
	})
}
