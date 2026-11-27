package hostcfg

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/cloudflare"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func testCertPEM(zoneID, token string) string {
	raw, _ := json.Marshal(map[string]string{
		"zoneID":    zoneID,
		"accountID": "acc123",
		"apiToken":  token,
	})
	return string(pem.EncodeToMemory(&pem.Block{Type: "ARGO TUNNEL TOKEN", Bytes: raw}))
}

func TestTunnelLoginZoneCaptureAndEnforcement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/v4/zones/z123" || r.Header.Get("Authorization") != "Bearer tok-secret" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"name":"Example.Net"}}`))
	}))
	defer srv.Close()

	s, _, _ := newService(t)
	s.cfAPIBase = srv.URL
	s.cfHTTP = srv.Client()

	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	cert := testCertPEM("z123", "tok-secret")
	f.On("cloudflared", "tunnel", "login").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		c.OnLine("stdout", "https://dash.cloudflare.com/argotunnel?aud=1")
		home := strings.TrimPrefix(c.Env[0], "HOME=")
		_ = os.MkdirAll(filepath.Join(home, ".cloudflared"), 0o700)
		return cmdrunner.Result{}, os.WriteFile(filepath.Join(home, ".cloudflared", "cert.pem"), []byte(cert), 0o600)
	})
	f.On("cloudflared", "tunnel", "--origincert").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		i := slices.Index(c.Args, "--credentials-file")
		return cmdrunner.Result{}, os.WriteFile(c.Args[i+1], []byte(creds), 0o600)
	})

	ctx := context.Background()
	if _, err := s.SetHost(ctx, Host{Hostname: "homelab"}); err != nil {
		t.Fatal(err)
	}
	l, err := s.TunnelLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l.ExpiresAt == nil {
		t.Fatal("waiting login missing expires_at")
	}
	if l := waitLogin(t, s.TunnelLoginState); l.State != LoginDone {
		t.Fatalf("login = %+v", l)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tunnel.Zone != "example.net" || got.Tunnel.Name != "homelab" {
		t.Fatalf("tunnel = %+v, want zone=example.net name=homelab", got.Tunnel)
	}

	// Route outside the approved zone is refused.
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Enabled: true,
		Routes:  []Route{{Hostname: "photos.other.org", Target: "photos"}},
	}); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "example.net") {
		t.Fatalf("out-of-zone err = %v", err)
	}

	// Route inside the approved zone with Public=true succeeds and flows to Values().
	got, err = s.SetTunnel(ctx, TunnelSet{
		Enabled: true,
		Routes:  []Route{{Hostname: "photos.example.net", Target: "photos", Public: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Tunnel.Routes[0].Public || got.Tunnel.Zone != "example.net" || got.Tunnel.Name != "homelab" {
		t.Fatalf("saved tunnel = %+v", got.Tunnel)
	}
	vals, err := s.Values(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vals.Tunnel.PublicHostnames["photos.example.net"] == "" {
		t.Fatalf("PublicHostnames = %+v", vals.Tunnel.PublicHostnames)
	}

	// TunnelFromStore reads the same tunnel + PublicHostnames straight from SQLite.
	fromDB, ok := TunnelFromStore(ctx, s.st)
	if !ok || !fromDB.Enabled || fromDB.PublicHostnames["photos.example.net"] == "" || !slices.Equal(fromDB.Hostnames, []string{"photos.example.net"}) {
		t.Fatalf("TunnelFromStore = %+v, ok=%v", fromDB, ok)
	}

	// Forget clears name and zone.
	got, err = s.TunnelForget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tunnel.Zone != "" || got.Tunnel.Name != "" {
		t.Fatalf("after forget: %+v", got.Tunnel)
	}
	if _, ok := TunnelFromStore(ctx, s.st); ok {
		t.Fatal("TunnelFromStore still enabled after forget")
	}
}

func TestTunnelLoginCancel(t *testing.T) {
	s, _, _ := newService(t)
	f := cmdrunnertest.NewFake()
	s.SetRunner(f)
	started := make(chan struct{})
	f.On("cloudflared", "tunnel", "login").Do(func(c cmdrunner.Cmd) (cmdrunner.Result, error) {
		c.OnLine("stdout", "https://dash.cloudflare.com/argotunnel?aud=cancel")
		close(started)
		// Block until cancelled by TunnelLoginCancel.
		time.Sleep(5 * time.Second)
		return cmdrunner.Result{}, errors.New("cancelled")
	})

	l, err := s.TunnelLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if l.State != LoginWaiting || l.URL == "" {
		t.Fatalf("waiting login = %+v", l)
	}
	got, err := s.TunnelLoginCancel()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != LoginIdle || s.TunnelLoginState().State != LoginIdle {
		t.Fatalf("after cancel = %+v, state = %+v", got, s.TunnelLoginState())
	}
}

func TestTunnelStatusAndRecheckAndRestart(t *testing.T) {
	root := t.TempDir()
	f := cmdrunnertest.NewFake()
	f.On("systemctl").Reply("")
	f.On("cloudflared", "tunnel", "--origincert").Reply("")
	f.On("cloudflared", "tunnel", "--origincert", filepath.Join(root, "/var/lib/homelab/cloudflared/cert.pem"), "route", "dns", "--overwrite-dns", tunnelID(creds), "bad.example.net").
		FailCode(1, "record already exists")
	applier := &ImageApplier{Runner: f, Root: root}

	s, _, _ := newService(t)
	s.apply = applier
	s.SetRunner(f)
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.tunnelProbe.settle = 0

	ctx := context.Background()
	if _, err := s.SetDomain(ctx, DomainSet{Domain: "home.example.com"}); err != nil {
		t.Fatal(err)
	}
	s.SetStackRoutes(func(context.Context) []string { return []string{"photos.home.example.com"} })

	// Off before credentials.
	if st := s.TunnelStatus(ctx); st.State != TunnelOff {
		t.Fatalf("initial state = %+v", st)
	}

	// Store tunnel with cert + 2 routes (one matching a stack, one missing target & failing DNS).
	if err := s.storeTunnel(ctx, creds, "cert-body", "homelab", "example.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Enabled: true,
		Routes: []Route{
			{Hostname: "photos.example.net", Target: "photos"},
			{Hostname: "bad.example.net", Target: "ghost"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Configure fake systemd + metrics + probe.
	unitOut := "ActiveState=active\nActiveEnterTimestamp=@1795600000\n"
	f.On("systemctl", "show").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: unitOut}, nil
	})
	journalOut := "INF starting\n2026-11-26 ERR Failed to connect 1\nlevel=error msg=timeout 2\n"
	f.On("journalctl").Do(func(cmdrunner.Cmd) (cmdrunner.Result, error) {
		return cmdrunner.Result{Stdout: journalOut}, nil
	})

	var stats cloudflare.TunnelStats
	var statsErr error
	s.tunnelProbe.fetchStats = func(context.Context, string) (cloudflare.TunnelStats, error) {
		return stats, statsErr
	}
	s.tunnelProbe.probeHosts = func(_ context.Context, hosts []string, allow map[string]string) []cloudflare.HostnameStatus {
		out := make([]cloudflare.HostnameStatus, 0, len(hosts))
		for _, h := range hosts {
			if h == "photos.example.net" {
				if _, ok := allow[h]; ok {
					out = append(out, cloudflare.HostnameStatus{Hostname: h, Protection: cloudflare.Public, StatusCode: 200})
				} else {
					out = append(out, cloudflare.HostnameStatus{Hostname: h, Protection: cloudflare.Open, StatusCode: 200})
				}
			} else {
				out = append(out, cloudflare.HostnameStatus{Hostname: h, Protection: cloudflare.Unknown, Err: "no such host"})
			}
		}
		return out
	}

	// 1. Connected 4/4, probe populates open + unreachable, DNS shows added vs failed, NoTarget flagged on ghost.
	stats = cloudflare.TunnelStats{Ready: true, ReadyConnections: 4, EdgeLocations: []string{"ams", "fra"}, ConnectorID: "conn-1", Version: "2026.10.0"}
	s.probeAllTunnelRoutes(ctx)
	st := s.TunnelStatus(ctx)
	if st.State != TunnelConnected || st.Connections != 4 || st.Version != "2026.10.0" || len(st.Colos) != 2 || st.StartedAt == nil {
		t.Fatalf("connected status = %+v", st)
	}
	if len(st.Routes) != 2 {
		t.Fatalf("routes = %+v", st.Routes)
	}
	r0, r1 := st.Routes[0], st.Routes[1]
	if r0.DNS != RouteDNSAdded || r0.Protection != RouteProtectionOpen || r0.NoTarget {
		t.Errorf("route 0 = %+v", r0)
	}
	if r1.DNS != RouteDNSFailed || !strings.Contains(r1.DNSError, "already exists") || r1.Protection != RouteProtectionUnreachable || !r1.NoTarget {
		t.Errorf("route 1 = %+v", r1)
	}

	// 2. Marking photos Public flips protection to public immediately without re-probing.
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Enabled: true,
		Routes: []Route{
			{Hostname: "photos.example.net", Target: "photos", Public: true},
			{Hostname: "bad.example.net", Target: "ghost"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if st := s.TunnelStatus(ctx); st.Routes[0].Protection != RouteProtectionPublic {
		t.Fatalf("after marking public: route 0 = %+v", st.Routes[0])
	}

	// 3. Degraded (2/4) includes recent journal errors.
	stats.ReadyConnections = 2
	st = s.TunnelStatus(ctx)
	if st.State != TunnelDegraded || len(st.RecentErrors) != 2 {
		t.Fatalf("degraded status = %+v", st)
	}

	// 4. Down (metrics unreachable + unit inactive) -> down with recent errors.
	unitOut = "ActiveState=inactive\nActiveEnterTimestamp=@0\n"
	statsErr = errors.New("connection refused")
	st = s.TunnelStatus(ctx)
	if st.State != TunnelDown || len(st.RecentErrors) != 2 || st.StartedAt != nil {
		t.Fatalf("down status = %+v", st)
	}

	// 5. Starting (activating unit).
	unitOut = "ActiveState=activating\nActiveEnterTimestamp=@0\n"
	if st := s.TunnelStatus(ctx); st.State != TunnelStarting {
		t.Fatalf("starting status = %+v", st)
	}

	// 6. Restart runs systemctl restart homelab-cloudflared.service.
	unitOut = "ActiveState=active\nActiveEnterTimestamp=@1795600000\n"
	statsErr = nil
	stats.ReadyConnections = 4
	if st, err := s.TunnelRestart(ctx); err != nil || st.State != TunnelConnected {
		t.Fatalf("restart = %+v, err=%v", st, err)
	}
	if len(f.CallsMatching("systemctl", "restart", tunnelUnit)) == 0 {
		t.Fatal("systemctl restart not called")
	}

	// 7. Recheck updates one route and rejects unknown hostnames.
	if _, err := s.RecheckTunnelRoute(ctx, "unknown.example.net"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recheck unknown err = %v", err)
	}
	if st, err := s.RecheckTunnelRoute(ctx, "photos.example.net"); err != nil || st.Routes[0].Protection != RouteProtectionPublic {
		t.Fatalf("recheck photos = %+v, err=%v", st, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRunTunnelProbeWaitsForCloudflaredReady(t *testing.T) {
	s, _, _ := newService(t)
	s.tunnelProbe.every = time.Hour
	s.tunnelProbe.settle = 0
	s.tunnelProbe.readyWait = 2 * time.Second
	s.tunnelProbe.readyPoll = 2 * time.Millisecond

	ctx := context.Background()
	if err := s.storeTunnel(ctx, creds, "cert-body", "homelab", "example.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Enabled: true,
		Routes:  []Route{{Hostname: "photos.example.net", Target: "photos", Public: true}},
	}); err != nil {
		t.Fatal(err)
	}
	// Drain the kick queued by SetTunnel so only the explicit kick below runs a second cycle.
	select {
	case <-s.tunnelProbe.kick:
	default:
	}

	var fetchCalls atomic.Int32
	var ready atomic.Bool
	s.tunnelProbe.fetchStats = func(context.Context, string) (cloudflare.TunnelStats, error) {
		n := fetchCalls.Add(1)
		if !ready.Load() {
			if n <= 2 {
				return cloudflare.TunnelStats{}, errors.New("connection refused")
			}
			if n == 3 {
				return cloudflare.TunnelStats{Ready: false, ReadyConnections: 0}, nil
			}
			ready.Store(true)
		}
		return cloudflare.TunnelStats{Ready: true, ReadyConnections: 4}, nil
	}

	type probeEvent struct {
		callsAtProbe int32
	}
	probed := make(chan probeEvent, 4)
	s.tunnelProbe.probeHosts = func(_ context.Context, hosts []string, allow map[string]string) []cloudflare.HostnameStatus {
		probed <- probeEvent{callsAtProbe: fetchCalls.Load()}
		return []cloudflare.HostnameStatus{{Hostname: hosts[0], Protection: cloudflare.Public, StatusCode: 200}}
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunTunnelProbe(runCtx)
	}()

	select {
	case ev := <-probed:
		if ev.callsAtProbe < 4 {
			cancel()
			<-done
			t.Fatalf("probe ran after only %d fetchStats calls, want >= 4", ev.callsAtProbe)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for initial probe")
	}

	if st := s.TunnelStatus(ctx); st.Routes[0].Protection != RouteProtectionPublic {
		cancel()
		<-done
		t.Fatalf("route protection = %q, want public", st.Routes[0].Protection)
	}

	var kickCalls atomic.Int32
	ready.Store(false)
	s.tunnelProbe.fetchStats = func(context.Context, string) (cloudflare.TunnelStats, error) {
		n := kickCalls.Add(1)
		if n < 3 {
			return cloudflare.TunnelStats{Ready: false, ReadyConnections: 0}, nil
		}
		return cloudflare.TunnelStats{Ready: true, ReadyConnections: 4}, nil
	}
	s.kickTunnelProbe(true)

	select {
	case <-probed:
		if got := kickCalls.Load(); got < 3 {
			cancel()
			<-done
			t.Fatalf("kicked probe ran after only %d fetchStats calls, want >= 3", got)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for kicked probe")
	}

	cancel()
	<-done
}

func TestRunTunnelProbeProceedsWhenReadyTimesOut(t *testing.T) {
	s, _, _ := newService(t)
	s.tunnelProbe.every = time.Hour
	s.tunnelProbe.settle = 0
	s.tunnelProbe.readyWait = 25 * time.Millisecond
	s.tunnelProbe.readyPoll = 5 * time.Millisecond

	ctx := context.Background()
	if err := s.storeTunnel(ctx, creds, "cert-body", "homelab", "example.net"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetTunnel(ctx, TunnelSet{
		Enabled: true,
		Routes:  []Route{{Hostname: "photos.example.net", Target: "photos"}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.tunnelProbe.kick:
	default:
	}

	var fetchCalls atomic.Int32
	s.tunnelProbe.fetchStats = func(context.Context, string) (cloudflare.TunnelStats, error) {
		fetchCalls.Add(1)
		return cloudflare.TunnelStats{}, errors.New("connection refused")
	}
	probed := make(chan struct{}, 2)
	s.tunnelProbe.probeHosts = func(_ context.Context, hosts []string, _ map[string]string) []cloudflare.HostnameStatus {
		select {
		case probed <- struct{}{}:
		default:
		}
		return []cloudflare.HostnameStatus{{Hostname: hosts[0], Protection: cloudflare.Unknown, Err: "dial failed"}}
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunTunnelProbe(runCtx)
	}()

	select {
	case <-probed:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("timed out waiting for probe after ready timeout")
	}

	cancel()
	<-done

	if fetchCalls.Load() == 0 {
		t.Fatal("expected fetchStats to be polled before timeout")
	}
	if st := s.TunnelStatus(ctx); st.Routes[0].Protection != RouteProtectionUnreachable {
		t.Fatalf("route protection = %q, want unreachable", st.Routes[0].Protection)
	}
}

func TestRunTunnelProbeSkipsWaitWhenTunnelOff(t *testing.T) {
	s, _, _ := newService(t)
	s.tunnelProbe.every = time.Hour
	s.tunnelProbe.settle = 0
	s.tunnelProbe.readyWait = 5 * time.Second
	s.tunnelProbe.readyPoll = 5 * time.Millisecond

	var fetchCalls atomic.Int32
	s.tunnelProbe.fetchStats = func(context.Context, string) (cloudflare.TunnelStats, error) {
		fetchCalls.Add(1)
		return cloudflare.TunnelStats{}, nil
	}
	var probeCalls atomic.Int32
	s.tunnelProbe.probeHosts = func(_ context.Context, hosts []string, _ map[string]string) []cloudflare.HostnameStatus {
		probeCalls.Add(1)
		return nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunTunnelProbe(runCtx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunTunnelProbe blocked while tunnel was off")
	}
	if fetchCalls.Load() != 0 {
		t.Fatalf("fetchCalls = %d, want 0", fetchCalls.Load())
	}
	if probeCalls.Load() != 0 {
		t.Fatalf("probeCalls = %d, want 0", probeCalls.Load())
	}
}

func TestBackfillTunnelZoneRetriesTransientAndStopsOnPermanent(t *testing.T) {
	ctx := context.Background()
	id := tunnelID(creds)

	// 1. Transient HTTP 503 retries and persists zone on 200, then stops calling API.
	t.Run("transient 503 then 200", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hits.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":{"name":"Example.Net"}}`))
		}))
		defer srv.Close()

		s, _, _ := newService(t)
		s.cfAPIBase = srv.URL
		s.cfHTTP = srv.Client()
		if err := s.storeTunnel(ctx, creds, testCertPEM("z123", "tok"), "homelab", ""); err != nil {
			t.Fatal(err)
		}

		s.backfillTunnelZone(ctx)
		if got, _ := s.Get(ctx); got.Tunnel.Zone != "" {
			t.Fatalf("after 503 zone = %q, want empty", got.Tunnel.Zone)
		}
		s.zoneMu.Lock()
		checked := s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != "" {
			t.Fatalf("after 503 zoneCheckedFor = %q, want empty", checked)
		}

		s.backfillTunnelZone(ctx)
		if got, _ := s.Get(ctx); got.Tunnel.Zone != "example.net" {
			t.Fatalf("after 200 zone = %q, want example.net", got.Tunnel.Zone)
		}
		s.zoneMu.Lock()
		checked = s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != id {
			t.Fatalf("after 200 zoneCheckedFor = %q, want %q", checked, id)
		}

		s.backfillTunnelZone(ctx)
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2 (no extra API call after caching)", hits.Load())
		}
	})

	// 2. Transient network error retries and succeeds on next call.
	t.Run("transient network error then 200", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"success":true,"result":{"name":"example.net"}}`))
		}))
		defer srv.Close()

		s, _, _ := newService(t)
		s.cfAPIBase = srv.URL
		s.cfHTTP = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if hits.Add(1) == 1 {
					return nil, errors.New("dial tcp: i/o timeout")
				}
				return srv.Client().Transport.RoundTrip(req)
			}),
		}
		if err := s.storeTunnel(ctx, creds, testCertPEM("z123", "tok"), "homelab", ""); err != nil {
			t.Fatal(err)
		}

		s.backfillTunnelZone(ctx)
		if got, _ := s.Get(ctx); got.Tunnel.Zone != "" {
			t.Fatalf("after network error zone = %q, want empty", got.Tunnel.Zone)
		}
		s.zoneMu.Lock()
		checked := s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != "" {
			t.Fatalf("after network error zoneCheckedFor = %q, want empty", checked)
		}

		s.backfillTunnelZone(ctx)
		if got, _ := s.Get(ctx); got.Tunnel.Zone != "example.net" {
			t.Fatalf("after retry zone = %q, want example.net", got.Tunnel.Zone)
		}
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2", hits.Load())
		}
	})

	// 3. Permanent HTTP 403 stops retrying; storeTunnel clears the cache for a new login.
	t.Run("permanent 403 cached and reset by storeTunnel", func(t *testing.T) {
		var hits atomic.Int32
		var allow atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if !allow.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":{"name":"example.net"}}`))
		}))
		defer srv.Close()

		s, _, _ := newService(t)
		s.cfAPIBase = srv.URL
		s.cfHTTP = srv.Client()
		if err := s.storeTunnel(ctx, creds, testCertPEM("z123", "tok"), "homelab", ""); err != nil {
			t.Fatal(err)
		}

		s.backfillTunnelZone(ctx)
		s.backfillTunnelZone(ctx)
		if hits.Load() != 1 {
			t.Fatalf("hits after 403 + second backfill = %d, want 1", hits.Load())
		}
		s.zoneMu.Lock()
		checked := s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != id {
			t.Fatalf("after 403 zoneCheckedFor = %q, want %q", checked, id)
		}

		if err := s.storeTunnel(ctx, `{"TunnelID":"bad"}`, testCertPEM("z123", "tok"), "homelab", ""); err == nil {
			t.Fatal("expected storeTunnel with bad credentials to fail")
		}
		s.zoneMu.Lock()
		checked = s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != id {
			t.Fatalf("failed storeTunnel cleared zoneCheckedFor = %q, want %q", checked, id)
		}

		if err := s.storeTunnel(ctx, creds, testCertPEM("z123", "new-tok"), "homelab", ""); err != nil {
			t.Fatal(err)
		}
		s.zoneMu.Lock()
		checked = s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != "" {
			t.Fatalf("after storeTunnel zoneCheckedFor = %q, want empty", checked)
		}

		allow.Store(true)
		s.backfillTunnelZone(ctx)
		if got, _ := s.Get(ctx); got.Tunnel.Zone != "example.net" {
			t.Fatalf("after fresh storeTunnel zone = %q, want example.net", got.Tunnel.Zone)
		}
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2", hits.Load())
		}
	})

	// 4. Invalid cert makes no HTTP request and caches permanent failure.
	t.Run("invalid cert cached without HTTP", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
		}))
		defer srv.Close()

		s, _, _ := newService(t)
		s.cfAPIBase = srv.URL
		s.cfHTTP = srv.Client()
		if err := s.storeTunnel(ctx, creds, "not-a-pem", "homelab", ""); err != nil {
			t.Fatal(err)
		}

		s.backfillTunnelZone(ctx)
		if hits.Load() != 0 {
			t.Fatalf("hits = %d, want 0", hits.Load())
		}
		s.zoneMu.Lock()
		checked := s.zoneCheckedFor
		s.zoneMu.Unlock()
		if checked != id {
			t.Fatalf("zoneCheckedFor = %q, want %q", checked, id)
		}
	})
}
