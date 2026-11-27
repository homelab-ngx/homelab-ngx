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
	tsRunning    = `{"BackendState":"Running","AuthURL":"","Version":"1.104.7","CurrentTailnet":{"Name":"ops@example.com"},"Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":["100.101.102.103","fd7a:115c:a1e0::1"],"AllowedIPs":["100.101.102.103/32","fd7a:115c:a1e0::1/128"]}}`
)

func TestParseTailscale(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	expFuture := time.Date(2027, 5, 14, 0, 0, 0, 0, time.UTC)
	expPast := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	t.Run("basic states", func(t *testing.T) {
		for _, c := range []struct {
			name string
			in   string
			cfg  Tailscale
			want TailscaleStatus
		}{
			{"empty", "", Tailscale{}, TailscaleStatus{State: TailscaleOff}},
			{"starting", `{"BackendState":"Starting"}`, Tailscale{}, TailscaleStatus{State: TailscaleStarting, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"nostate", `{"BackendState":"NoState"}`, Tailscale{}, TailscaleStatus{State: TailscaleStarting, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"needs-login", tsNeedsLogin, Tailscale{}, TailscaleStatus{State: TailscaleNeedsLogin, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"needs-login-url", `{"BackendState":"NeedsLogin","AuthURL":"https://login.tailscale.com/a/abc"}`, Tailscale{}, TailscaleStatus{State: TailscaleNeedsLogin, AuthURL: "https://login.tailscale.com/a/abc", ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"needs-approval", `{"BackendState":"NeedsMachineAuth","Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":["100.101.102.103"]}}`, Tailscale{ExitNode: true, AdvertiseRoutes: []string{"192.168.1.0/24"}}, TailscaleStatus{State: TailscaleNeedsApproval, DNSName: "box.tail1234.ts.net", IPs: []string{"100.101.102.103"}, Routes: []TailscaleRoute{{Prefix: "192.168.1.0/24", Approved: false}}, ExitNode: TailscaleExitPending, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"stopped", `{"BackendState":"Stopped","CurrentTailnet":{"Name":"ops@example.com"},"Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":["100.101.102.103"]}}`, Tailscale{}, TailscaleStatus{State: TailscaleStopped, Tailnet: "ops@example.com", DNSName: "box.tail1234.ts.net", IPs: []string{"100.101.102.103"}, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"expired-flag", `{"BackendState":"NeedsLogin","Self":{"DNSName":"box.tail1234.ts.net.","TailscaleIPs":["100.101.102.103"],"Expired":true}}`, Tailscale{}, TailscaleStatus{State: TailscaleExpired, DNSName: "box.tail1234.ts.net", IPs: []string{"100.101.102.103"}, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"expired-time", `{"BackendState":"Running","Self":{"DNSName":"box.tail1234.ts.net.","KeyExpiry":"` + expPast.Format(time.RFC3339) + `"}}`, Tailscale{}, TailscaleStatus{State: TailscaleExpired, DNSName: "box.tail1234.ts.net", KeyExpiry: &expPast, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
			{"running-zero-expiry", tsRunning, Tailscale{}, TailscaleStatus{State: TailscaleRunning, Version: "1.104.7", Tailnet: "ops@example.com", DNSName: "box.tail1234.ts.net", IPs: []string{"100.101.102.103", "fd7a:115c:a1e0::1"}, ControlURL: tailscaleDefaultControlURL, AdminURL: tailscaleAdminURL}},
		} {
			got := parseTailscale(c.in, c.cfg, now)
			if got.State != c.want.State || got.AuthURL != c.want.AuthURL || got.Tailnet != c.want.Tailnet ||
				got.DNSName != c.want.DNSName || got.Version != c.want.Version ||
				got.ControlURL != c.want.ControlURL || got.AdminURL != c.want.AdminURL {
				t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
			}
			if c.want.IPs != nil && !slices.Equal(got.IPs, c.want.IPs) {
				t.Errorf("%s: ips = %v, want %v", c.name, got.IPs, c.want.IPs)
			}
			if c.want.Routes != nil && !slices.Equal(got.Routes, c.want.Routes) {
				t.Errorf("%s: routes = %+v, want %+v", c.name, got.Routes, c.want.Routes)
			}
			if c.want.ExitNode != "" && got.ExitNode != c.want.ExitNode {
				t.Errorf("%s: exit_node = %s, want %s", c.name, got.ExitNode, c.want.ExitNode)
			}
			if (got.KeyExpiry == nil) != (c.want.KeyExpiry == nil) {
				t.Errorf("%s: key_expiry = %v, want %v", c.name, got.KeyExpiry, c.want.KeyExpiry)
			}
		}
	})

	t.Run("running rich telemetry and routes", func(t *testing.T) {
		raw := `{
			"BackendState":"Running",
			"Version":"1.104.7",
			"Health":["Tailscale could not reach the control server.", ""],
			"CurrentTailnet":{"Name":"example.github"},
			"Self":{
				"DNSName":"box.tail1234.ts.net.",
				"TailscaleIPs":["100.101.2.3","fd7a:115c:a1e0::1"],
				"AllowedIPs":["100.101.2.3/32","fd7a:115c:a1e0::1/128","192.168.1.0/24","0.0.0.0/0","::/0"],
				"KeyExpiry":"` + expFuture.Format(time.RFC3339) + `",
				"Tags":["tag:server"],
				"Relay":"fra",
				"UserID":42,
				"ExitNodeOption":true
			},
			"User":{"42":{"LoginName":"tagged-devices","DisplayName":"Tagged"},"99":{"LoginName":"alice@example.com"}},
			"Peer":{
				"a":{"HostName":"phone","DNSName":"phone.tail1234.ts.net.","Online":true,"CurAddr":"1.2.3.4:41641"},
				"b":{"HostName":"nas","Online":false}
			}
		}`
		cfg := Tailscale{
			Enabled:         true,
			AdvertiseRoutes: []string{"192.168.1.0/24", "10.0.20.0/24"},
			ExitNode:        true,
			LoginServer:     "https://headscale.example.net",
		}
		got := parseTailscale(raw, cfg, now)
		if got.State != TailscaleRunning || got.Version != "1.104.7" || !got.KeyExpiry.Equal(expFuture) ||
			!slices.Equal(got.Tags, []string{"tag:server"}) || got.User != "" || got.Relay != "fra" ||
			!slices.Equal(got.Health, []string{"Tailscale could not reach the control server."}) ||
			got.ExitNode != TailscaleExitApproved || got.ControlURL != "https://headscale.example.net" ||
			got.AdminURL != "" || got.OnlinePeers != 1 || got.TotalPeers != 2 {
			t.Fatalf("got = %+v", got)
		}
		wantRoutes := []TailscaleRoute{{Prefix: "192.168.1.0/24", Approved: true}, {Prefix: "10.0.20.0/24", Approved: false}}
		if !slices.Equal(got.Routes, wantRoutes) {
			t.Fatalf("routes = %+v, want %+v", got.Routes, wantRoutes)
		}
	})
}

func TestParseTailscalePeers(t *testing.T) {
	t.Parallel()
	now := "2026-11-27T11:55:00Z"
	raw := `{
		"Peer":{
			"1":{"HostName":"nas-old","Online":false,"Relay":"","LastSeen":"` + now + `"},
			"2":{"HostName":"phone","DNSName":"phone.tail1234.ts.net.","OS":"iOS","TailscaleIPs":["100.64.0.2"],"Online":true,"CurAddr":"198.51.100.1:41641"},
			"3":{"HostName":"laptop","DNSName":"laptop.tail1234.ts.net.","OS":"linux","TailscaleIPs":["100.64.0.3"],"Online":true,"Relay":"fra","LastSeen":"0001-01-01T00:00:00Z"}
		}
	}`
	got := parseTailscalePeers(raw)
	if len(got.Peers) != 3 {
		t.Fatalf("peers = %+v", got.Peers)
	}
	if got.Peers[0].Name != "laptop" || got.Peers[0].Direct || got.Peers[0].Relay != "fra" || got.Peers[0].LastSeen != nil {
		t.Errorf("peer[0] = %+v", got.Peers[0])
	}
	if got.Peers[1].Name != "phone" || !got.Peers[1].Direct || !got.Peers[1].Online || got.Peers[1].OS != "iOS" {
		t.Errorf("peer[1] = %+v", got.Peers[1])
	}
	if got.Peers[2].Name != "nas-old" || got.Peers[2].Online || got.Peers[2].LastSeen == nil {
		t.Errorf("peer[2] = %+v", got.Peers[2])
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
	st, err := s.TailscaleLogin(ctx, TailscaleLoginInput{})
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
	if _, err := s.TailscaleLogin(ctx, TailscaleLoginInput{}); err != nil || len(f.CallsMatching("tailscale", "login")) != 1 {
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
	_, err := s.TailscaleLogin(context.Background(), TailscaleLoginInput{})
	if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("err = %v", err)
	}
}

func TestTailscaleLoginAuthKeyAndServer(t *testing.T) {
	const secretKey = "tskey-auth-super-secret-value"
	s, store, rec := newService(t)
	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	statusReply := tsNeedsLogin
	f.On("tailscale", "status").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: statusReply}, nil
	})
	var keyFilePath string
	f.On("tailscale", "login").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		for _, a := range c.Args {
			if strings.Contains(a, secretKey) {
				t.Errorf("auth key leaked in argv: %v", c.Args)
			}
			if p, ok := strings.CutPrefix(a, "--auth-key=file:"); ok {
				keyFilePath = p
				fi, err := os.Stat(p)
				if err != nil || fi.Mode().Perm() != 0o600 {
					t.Errorf("key file stat = %v, %v", fi, err)
				}
				b, _ := os.ReadFile(p)
				if string(b) != secretKey {
					t.Errorf("key file contents = %q", b)
				}
			}
		}
		if !slices.Contains(c.Args, "--login-server=https://hs.example.net") {
			t.Errorf("missing login-server in %v", c.Args)
		}
		statusReply = tsRunning
		return cmdrunner.Result{}, nil
	})

	ctx := context.Background()
	srv := "https://hs.example.net/"
	st, err := s.TailscaleLogin(ctx, TailscaleLoginInput{AuthKey: secretKey, LoginServer: &srv})
	if err != nil {
		t.Fatal(err)
	}
	if keyFilePath == "" {
		t.Fatal("--auth-key=file:... not passed")
	}
	if _, err := os.Stat(keyFilePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp key file was not removed: %s", keyFilePath)
	}
	if st.ControlURL != "https://hs.example.net" || len(rec.got) == 0 {
		t.Fatalf("status = %+v, applies = %d", st, len(rec.got))
	}
	rows, _ := store.Queries().ListHostSettings(ctx)
	for _, r := range rows {
		if strings.Contains(r.Value, secretKey) || strings.Contains(string(r.Secret), secretKey) {
			t.Fatal("auth key persisted in SQLite")
		}
	}

	// Changing login server while connected requires force_reauth.
	other := "https://hs2.example.net"
	if _, err := s.TailscaleLogin(ctx, TailscaleLoginInput{LoginServer: &other}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changing server without force_reauth = %v, want invalid", err)
	}
	f.On("tailscale", "up").Reply("")
	if _, err := s.TailscaleLogin(ctx, TailscaleLoginInput{LoginServer: &other, ForceReauth: true}); err != nil {
		t.Fatalf("force_reauth change = %v", err)
	}
	upCalls := f.CallsMatching("tailscale", "up")
	if len(upCalls) != 1 || !slices.Contains(upCalls[0].Args, "--force-reauth") || !slices.Contains(upCalls[0].Args, "--login-server=https://hs2.example.net") {
		t.Fatalf("up calls = %+v", upCalls)
	}
}

func TestTailscaleLoginAuthKeyErrorRedacted(t *testing.T) {
	const secretKey = "tskey-auth-bad-secret"
	s, _, _ := newService(t)
	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	f.On("tailscale", "status").Reply(tsNeedsLogin)
	f.On("tailscale", "login").FailCode(1, "invalid key: "+secretKey)
	_, err := s.TailscaleLogin(context.Background(), TailscaleLoginInput{AuthKey: secretKey})
	if !errors.Is(err, ErrUpstream) || !strings.Contains(err.Error(), "invalid key") || strings.Contains(err.Error(), secretKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestTailscaleForceReauthWhileRunningSurfacesAuthURL(t *testing.T) {
	s, _, _ := newService(t)
	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	f.On("tailscale", "status").Reply(tsRunning)
	done := make(chan struct{})
	f.On("tailscale", "up").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		c.OnLine("stderr", "To authenticate, visit:")
		c.OnLine("stderr", "\thttps://login.tailscale.com/a/reauth123")
		<-done
		return cmdrunner.Result{}, nil
	})

	st, err := s.TailscaleLogin(context.Background(), TailscaleLoginInput{ForceReauth: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != TailscaleRunning || st.AuthURL != "https://login.tailscale.com/a/reauth123" {
		t.Fatalf("status = %+v", st)
	}
	close(done)
	waitLogin(t, s.tailscaleLogin.get)
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

// Offered subnets need the host to forward their family; nothing else turns
// it on while no stack runs. A host not logged in is left as it is.
func TestTailscaleForwardsOfferedSubnets(t *testing.T) {
	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	f.On("systemctl").Reply("")
	status := tsNeedsLogin
	f.On("tailscale", "status").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) { return cmdrunner.Result{Stdout: status}, nil })
	f.On("tailscale", "set").Reply("")
	a := &ImageApplier{Runner: f, Root: root}
	s := snapshotOf(t)
	s.Tailscale = Tailscale{Enabled: true, AdvertiseRoutes: []string{"192.168.1.0/24"}}
	v4, v6 := filepath.Join(root, "proc/sys/net/ipv4/ip_forward"), filepath.Join(root, "proc/sys/net/ipv6/conf/all/forwarding")
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	ctx := context.Background()
	if err := a.tailscale(ctx, s); err != nil {
		t.Fatal(err)
	}
	if read(v4) != "" {
		t.Errorf("forwarding turned on before the login: %q", read(v4))
	}
	status = tsRunning
	if err := a.tailscale(ctx, s); err != nil {
		t.Fatal(err)
	}
	if read(v4) != "1" || read(v6) != "" {
		t.Errorf("IPv4 subnet: ip_forward=%q, IPv6 forwarding=%q", read(v4), read(v6))
	}
	s.Tailscale.AdvertiseRoutes = append(s.Tailscale.AdvertiseRoutes, "fd00:1::/64")
	if err := a.tailscale(ctx, s); err != nil {
		t.Fatal(err)
	}
	if read(v6) != "1" {
		t.Errorf("IPv6 subnet: IPv6 forwarding=%q", read(v6))
	}

	// Set pins --accept-routes=false and prefs; exit node enables IPv4 + IPv6 forwarding.
	setCalls := f.CallsMatching("tailscale", "set")
	lastSet := setCalls[len(setCalls)-1].Args
	for _, want := range []string{"--accept-routes=false", "--accept-dns=true", "--advertise-exit-node=false", "--ssh=false"} {
		if !slices.Contains(lastSet, want) {
			t.Errorf("set args %v missing %s", lastSet, want)
		}
	}

	root2 := t.TempDir()
	f2 := cmdrunnertest.NewFake()
	f2.On("systemctl").Reply("")
	f2.On("tailscale", "status").Reply(`{"BackendState":"Stopped"}`)
	f2.On("tailscale", "up").Reply("")
	a2 := &ImageApplier{Runner: f2, Root: root2}
	s2 := snapshotOf(t)
	s2.Tailscale = Tailscale{Enabled: true, ExitNode: true, AcceptDNS: boolPtr(false), SSH: true}
	if err := a2.tailscale(ctx, s2); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root2, "proc/sys/net/ipv4/ip_forward")); string(b) != "1" {
		t.Errorf("exit node ip_forward = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root2, "proc/sys/net/ipv6/conf/all/forwarding")); string(b) != "1" {
		t.Errorf("exit node ipv6 forwarding = %q", b)
	}
	upCalls := f2.CallsMatching("tailscale", "up")
	if len(upCalls) != 1 || !slices.Contains(upCalls[0].Args, "--advertise-exit-node=true") ||
		!slices.Contains(upCalls[0].Args, "--accept-dns=false") ||
		!slices.Contains(upCalls[0].Args, "--ssh=true") ||
		!slices.Contains(upCalls[0].Args, "--accept-routes=false") {
		t.Errorf("stopped up args = %+v", upCalls)
	}
}
