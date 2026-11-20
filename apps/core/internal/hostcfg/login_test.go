package hostcfg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

const (
	tsNeedsLogin = `{"BackendState":"NeedsLogin","AuthURL":"","Self":{"DNSName":"","TailscaleIPs":null}}`
	tsRunning    = `{"BackendState":"Running","AuthURL":"","CurrentTailnet":{"Name":"ops@example.com"},"Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":["100.101.102.103","fd7a:115c:a1e0::1"]}}`
)

func TestParseTailscale(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want TailscaleStatus
	}{
		{"", TailscaleStatus{State: TailscaleOff, IPs: []string{}}},
		{tsNeedsLogin, TailscaleStatus{State: TailscaleNeedsLogin, IPs: []string{}}},
		{`{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/abc"}`, TailscaleStatus{State: TailscaleNeedsLogin, AuthURL: "https://login.tailscale.com/a/abc", IPs: []string{}}},
		{`{"BackendState":"NeedsMachineAuth"}`, TailscaleStatus{State: TailscaleNeedsApproval, IPs: []string{}}},
		{`{"BackendState":"Stopped"}`, TailscaleStatus{State: TailscaleNeedsLogin, IPs: []string{}}},
		{tsRunning, TailscaleStatus{State: TailscaleRunning, Tailnet: "ops@example.com", DNSName: "box.tail1234.ts.net", IPs: []string{"100.101.102.103", "fd7a:115c:a1e0::1"}}},
	} {
		got := parseTailscale(c.in)
		if got.State != c.want.State || got.AuthURL != c.want.AuthURL || got.Tailnet != c.want.Tailnet || got.DNSName != c.want.DNSName || !slices.Equal(got.IPs, c.want.IPs) {
			t.Errorf("parseTailscale(%s) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// waitLogin waits for a login to end.
func waitLogin(t *testing.T, get func() Login) Login {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if l := get(); l.State != LoginWaiting {
			return l
		}
	}
	t.Fatal("the login did not end")
	return Login{}
}

// The login turns Tailscale on, answers with the link tailscale login
// prints, and applies the settings again once the host is in.
func TestTailscaleLogin(t *testing.T) {
	s, _, rec := newService(t)
	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	f.On("tailscale", "status").Reply(tsNeedsLogin)
	approve := make(chan struct{})
	f.On("tailscale", "login").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		c.OnLine("stderr", "To authenticate, visit:")
		c.OnLine("stderr", "\thttps://login.tailscale.com/a/1a2b3c")
		<-approve
		return cmdrunner.Result{}, nil
	})
	ctx := context.Background()
	st, err := s.TailscaleLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != TailscaleNeedsLogin || st.AuthURL != "https://login.tailscale.com/a/1a2b3c" {
		t.Fatalf("status = %+v", st)
	}
	if got, _ := s.Get(ctx); !got.Tailscale.Enabled {
		t.Error("Tailscale was not turned on")
	}
	// A second start while the first waits starts nothing.
	if _, err := s.TailscaleLogin(ctx); err != nil || len(f.CallsMatching("tailscale", "login")) != 1 {
		t.Errorf("a second login started: %v", err)
	}
	applies := len(rec.got)
	close(approve)
	if l := waitLogin(t, s.tailscaleLogin.get); l.State != LoginDone {
		t.Fatalf("login = %+v", l)
	}
	if len(rec.got) != applies+1 {
		t.Error("the settings were not applied again after the login")
	}
}

func TestTailscaleLoginFails(t *testing.T) {
	s, _, _ := newService(t)
	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	f.On("tailscale", "status").Reply(tsNeedsLogin)
	f.On("tailscale", "login").FailCode(1, "backend error: invalid key")
	_, err := s.TailscaleLogin(context.Background())
	if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("err = %v", err)
	}
}

// fakeCloudflared answers tunnel login by writing the certificate under
// $HOME, and create (or, when exists, token) by writing the credentials.
func fakeCloudflared(f *cmdrunnertest.Fake, exists bool) {
	f.On("cloudflared", "tunnel", "login").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		c.OnLine("stdout", "Please open the following URL and log in with your Cloudflare account:")
		c.OnLine("stdout", "https://dash.cloudflare.com/argotunnel?aud=&callback=https%3A%2F%2Flogin.cloudflareaccess.org%2Fabc")
		home := strings.TrimPrefix(c.Env[0], "HOME=")
		_ = os.MkdirAll(filepath.Join(home, ".cloudflared"), 0o700)
		return cmdrunner.Result{}, os.WriteFile(filepath.Join(home, ".cloudflared", "cert.pem"), []byte("-----BEGIN ARGO TUNNEL TOKEN-----\n"), 0o600)
	})
	writeCreds := func(c cmdrunner.Cmd, flag string) (cmdrunner.Result, error) {
		i := slices.Index(c.Args, flag)
		return cmdrunner.Result{}, os.WriteFile(c.Args[i+1], []byte(creds), 0o600)
	}
	f.On("cloudflared", "tunnel", "--origincert").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		switch {
		case slices.Contains(c.Args, "token"):
			return writeCreds(c, "--cred-file")
		case exists:
			res := cmdrunner.Result{ExitCode: 1, Stderr: "failed to create tunnel: Create Tunnel API call failed: tunnel with name already exists"}
			return res, &cmdrunner.Error{Result: res, Err: errors.New("exit status 1")}
		}
		return writeCreds(c, "--credentials-file")
	})
}

// Connecting through Cloudflare's login stores the tunnel with its
// certificate and turns it on; the routes stay.
func TestTunnelLogin(t *testing.T) {
	for _, exists := range []bool{false, true} {
		s, _, _ := newService(t)
		f := cmdrunnertest.NewFake()
		s.SetRunner(f)
		fakeCloudflared(f, exists)
		ctx := context.Background()
		if _, err := s.SetHost(ctx, Host{Hostname: "box"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetTunnel(ctx, TunnelSet{Routes: []Route{{Hostname: "photos.example.net", Target: "photos"}}}); err != nil {
			t.Fatal(err)
		}
		l, err := s.TunnelLogin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(l.URL, "https://dash.cloudflare.com/argotunnel") {
			t.Errorf("login url = %q", l.URL)
		}
		if l := waitLogin(t, s.TunnelLoginState); l.State != LoginDone {
			t.Fatalf("exists=%v: login = %+v", exists, l)
		}
		got, _ := s.Get(ctx)
		if !got.Tunnel.Enabled || !got.Tunnel.DNSManaged || got.Tunnel.TunnelID != tunnelID(creds) || len(got.Tunnel.Routes) != 1 {
			t.Errorf("exists=%v: tunnel = %+v", exists, got.Tunnel)
		}
		if c := f.CallsMatching("cloudflared", "tunnel", "--origincert"); !slices.Contains(c[0].Args, "box") {
			t.Errorf("the tunnel is not named after the host: %v", c[0].Args)
		}
		// Pasted credentials keep the zone's certificate; forgetting drops both.
		other := strings.Replace(creds, "0f1e2d3c", "aaaaaaaa", 1)
		if got, _ := s.SetTunnel(ctx, TunnelSet{Enabled: true, Routes: got.Tunnel.Routes, Credentials: &other}); !got.Tunnel.DNSManaged {
			t.Error("new credentials dropped the certificate")
		}
		if got, _ := s.TunnelForget(ctx); got.Tunnel.Enabled || got.Tunnel.CredentialsSet || got.Tunnel.DNSManaged || len(got.Tunnel.Routes) != 1 {
			t.Errorf("after forget: %+v", got.Tunnel)
		}
	}
}

// A connected tunnel adds each route's DNS record once per daemon start;
// a route that fails is named and tried again on the next apply.
func TestTunnelRouteDNS(t *testing.T) {
	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	f.On("systemctl").Reply("")
	f.On("cloudflared", "tunnel", "--origincert").Reply("")
	f.On("cloudflared", "tunnel", "--origincert", filepath.Join(root, "/var/lib/homelab/cloudflared/cert.pem"), "route", "dns", "--overwrite-dns", tunnelID(creds), "books.example.net").
		FailCode(1, "Failed to add route: code: 1003, reason: An A, AAAA, or CNAME record with that host already exists.").Reply("")
	a := &ImageApplier{Runner: f, Root: root}
	s := snapshotOf(t)
	ctx := context.Background()
	// No certificate: no records.
	if err := a.tunnel(ctx, s); err != nil {
		t.Fatal(err)
	}
	if n := len(f.CallsMatching("cloudflared")); n != 0 {
		t.Fatalf("records added without a certificate: %d", n)
	}
	s.TunnelCert = "cert"
	err := a.tunnel(ctx, s)
	if err == nil || !strings.Contains(err.Error(), "books.example.net") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.CallsMatching("cloudflared")); n != 2 {
		t.Fatalf("route dns calls = %d", n)
	}
	if st, err := os.Stat(filepath.Join(root, "/var/lib/homelab/cloudflared/cert.pem")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("cert.pem: %v", err)
	}
	if err := a.tunnel(ctx, s); err != nil {
		t.Fatal(err)
	}
	if n := len(f.CallsMatching("cloudflared")); n != 3 {
		t.Errorf("route dns calls after a second apply = %d, want only the failed route again", n)
	}
}
