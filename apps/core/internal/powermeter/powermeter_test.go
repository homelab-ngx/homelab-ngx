package powermeter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"homelab-ngx.com/core/internal/agefile"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/store/storetest"
)

// fakeHA is a Home Assistant with one power sensor. Each connection gets
// the sensor's state, then whatever is sent on changes.
type fakeHA struct {
	t       *testing.T
	srv     *httptest.Server
	token   string
	entity  string
	state   string
	unit    string
	changes chan string // new states, sent as "+" changes

	mu    sync.Mutex
	conns int
	drop  chan struct{} // closed to cut every connection
}

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{t: t, token: "good", entity: "sensor.plug_power", state: "12.5", unit: "W", changes: make(chan string), drop: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHA) cut() {
	f.mu.Lock()
	close(f.drop)
	f.drop = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeHA) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func (f *fakeHA) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/websocket" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	f.mu.Lock()
	f.conns++
	drop, state, unit := f.drop, f.state, f.unit
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-drop:
			c.CloseNow()
		case <-ctx.Done():
		}
	}()
	_ = wsjson.Write(ctx, c, map[string]any{"type": "auth_required", "ha_version": "2026.11.0"})
	var m haMsg
	if wsjson.Read(ctx, c, &m) != nil {
		return
	}
	if m.AccessToken != f.token {
		_ = wsjson.Write(ctx, c, map[string]any{"type": "auth_invalid", "message": "Invalid access token or password"})
		return
	}
	_ = wsjson.Write(ctx, c, map[string]any{"type": "auth_ok"})
	if wsjson.Read(ctx, c, &m) != nil || m.Type != "subscribe_entities" {
		return
	}
	_ = wsjson.Write(ctx, c, map[string]any{"id": m.ID, "type": "result", "success": true, "result": nil})
	a := map[string]any{}
	for _, e := range m.EntityIDs {
		if e == f.entity {
			a[e] = map[string]any{"s": state, "a": map[string]any{"unit_of_measurement": unit, "friendly_name": "Server plug Power"}}
		}
	}
	_ = wsjson.Write(ctx, c, map[string]any{"id": m.ID, "type": "event", "event": map[string]any{"a": a}})
	go func() { // read, so pings are answered
		for {
			if _, _, err := c.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-f.changes:
			ev := map[string]any{"c": map[string]any{f.entity: map[string]any{"+": map[string]any{"s": s, "lu": 1}}}}
			_ = wsjson.Write(ctx, c, map[string]any{"id": m.ID, "type": "event", "event": ev})
		}
	}
}

func newService(t *testing.T) *Service {
	t.Helper()
	st, _ := storetest.Open(t)
	key := filepath.Join(t.TempDir(), "key.txt")
	if _, err := agefile.EnsureKey(key); err != nil {
		t.Fatal(err)
	}
	s := New(st, key, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.first, s.last = 20*time.Millisecond, 50*time.Millisecond
	return s
}

func ptr[T any](v T) *T { return &v }

// waitFor polls until cond holds or a second passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWsURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://ha.local:8123":    "ws://ha.local:8123/api/websocket",
		"https://example.org/ha/": "wss://example.org/ha/api/websocket",
		"http://192.0.2.10:8123/": "ws://192.0.2.10:8123/api/websocket",
	} {
		if got, err := wsURL(in); err != nil || got != want {
			t.Errorf("wsURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ha.local:8123", "ftp://ha.local", "http://user:pw@ha.local", "http://ha.local/?x=1"} {
		if _, err := wsURL(bad); err == nil {
			t.Errorf("wsURL(%q) took it", bad)
		}
	}
}

func TestUnits(t *testing.T) {
	for _, c := range []struct {
		state, unit string
		want        float64
		ok          bool
	}{
		{"12.5", "W", 12.5, true},
		{"0.0125", "kW", 12.5, true},
		{"12500", "mW", 12.5, true},
		{"unavailable", "W", 0, false},
		{"12.5", "kWh", 0, false},
		{"12.5", "", 0, false},
	} {
		got, ok := entity{state: c.state, unit: c.unit}.watts()
		if ok != c.ok || got != c.want {
			t.Errorf("%s %s = %v %v, want %v %v", c.state, c.unit, got, ok, c.want, c.ok)
		}
	}
}

func TestReadsAndFollowsTheEntity(t *testing.T) {
	ha := newFakeHA(t)
	s := newService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if _, err := s.Set(ctx, PowerMeterSet{URL: ha.srv.URL, Entity: ha.entity, Token: ptr("good")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first reading", func() bool { w, ok := s.Wall(); return ok && w == 12.5 })
	st, _ := s.Status(ctx)
	if st.State != StateConnected || st.Name != "Server plug Power" || st.Watts == nil || *st.Watts != 12.5 || !st.HasToken || st.At == 0 {
		t.Errorf("status %+v", st)
	}
	ha.changes <- "30"
	waitFor(t, "the change", func() bool { w, _ := s.Wall(); return w == 30 })
	ha.changes <- "unavailable"
	waitFor(t, "no reading", func() bool { _, ok := s.Wall(); return !ok })

	// A dropped connection is no reading until it is back.
	ha.changes <- "40"
	waitFor(t, "40", func() bool { w, _ := s.Wall(); return w == 40 })
	ha.cut()
	waitFor(t, "the reconnect", func() bool { return ha.connections() >= 2 })
	waitFor(t, "a reading again", func() bool { w, ok := s.Wall(); return ok && w == 12.5 })
}

func TestRefusedTokenWaitsForAChange(t *testing.T) {
	ha := newFakeHA(t)
	s := newService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	if _, err := s.Set(ctx, PowerMeterSet{URL: ha.srv.URL, Entity: ha.entity, Token: ptr("bad")}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "auth_failed", func() bool { st, _ := s.Status(ctx); return st.State == StateAuthFailed })
	time.Sleep(100 * time.Millisecond) // several backoff periods
	if n := ha.connections(); n != 1 {
		t.Errorf("%d connections after a refused token, want 1", n)
	}
	// Only idle_only changes: no reconnect.
	if _, err := s.Set(ctx, PowerMeterSet{URL: ha.srv.URL, Entity: ha.entity, IdleOnly: true}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := ha.connections(); n != 1 {
		t.Errorf("%d connections after idle_only, want 1", n)
	}
	if _, err := s.Set(ctx, PowerMeterSet{URL: ha.srv.URL, Entity: ha.entity, Token: ptr("good"), IdleOnly: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", func() bool { _, ok := s.Wall(); return ok })
	if !s.IdleOnly(ctx) {
		t.Error("idle_only lost")
	}
}

func TestTokenIsSealedAndNeverReturned(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	st, err := s.Set(ctx, PowerMeterSet{URL: "http://ha.local:8123", Entity: "sensor.plug_power", Token: ptr("secret-token")})
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasToken {
		t.Error("has_token false")
	}
	rs, _ := s.st.Queries().ListHostSettings(ctx)
	for _, r := range rs {
		if strings.Contains(r.Value, "secret-token") || bytes.Contains(r.Secret, []byte("secret-token")) {
			t.Errorf("the token is stored in the clear: %+v", r)
		}
	}
	// A fresh service opens it from the store.
	s2 := New(s.st, s.keyFile, s.log)
	if err := s2.load(ctx); err != nil || s2.token != "secret-token" {
		t.Errorf("reload: %q %v", s2.token, err)
	}
	// Absent keeps it; off removes it.
	if st, _ := s2.Set(ctx, PowerMeterSet{URL: "http://ha.local:8123", Entity: "sensor.plug_power"}); !st.HasToken {
		t.Error("an absent token removed the stored one")
	}
	if st, _ := s2.Set(ctx, PowerMeterSet{}); st.HasToken || st.State != StateOff || st.URL != "" {
		t.Errorf("off: %+v", st)
	}
}

func TestSetRefuses(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	for code, in := range map[string]PowerMeterSet{
		"invalid_url":    {URL: "ha.local", Entity: "sensor.x", Token: ptr("t")},
		"invalid_entity": {URL: "http://ha.local", Entity: "Plug Power", Token: ptr("t")},
		"token_required": {URL: "http://ha.local", Entity: "sensor.x"},
	} {
		_, err := s.Set(ctx, in)
		if p, ok := err.(*httpx.Problem); !ok || p.Code != code {
			t.Errorf("%s: %v", code, err)
		}
	}
}

func TestTest(t *testing.T) {
	ha := newFakeHA(t)
	s := newService(t)
	ctx := context.Background()
	got, err := s.Test(ctx, PowerMeterSet{URL: ha.srv.URL, Entity: ha.entity, Token: ptr("good")})
	if err != nil || !got.OK || got.Watts == nil || *got.Watts != 12.5 || got.Name != "Server plug Power" {
		t.Errorf("ok: %+v %v", got, err)
	}
	for code, in := range map[string]PowerMeterSet{
		"auth_invalid":     {URL: ha.srv.URL, Entity: ha.entity, Token: ptr("bad")},
		"entity_not_found": {URL: ha.srv.URL, Entity: "sensor.other", Token: ptr("good")},
		"unreachable":      {URL: "http://127.0.0.1:1", Entity: ha.entity, Token: ptr("good")},
	} {
		if got, err := s.Test(ctx, in); err != nil || got.OK || got.Code != code {
			t.Errorf("%s: %+v %v", code, got, err)
		}
	}
	ha.mu.Lock()
	ha.unit = "kWh"
	ha.mu.Unlock()
	if got, _ := s.Test(ctx, PowerMeterSet{URL: ha.srv.URL, Entity: ha.entity, Token: ptr("good")}); got.Code != "not_power" {
		t.Errorf("kWh: %+v", got)
	}
	// Nothing was saved.
	if st, _ := s.Status(ctx); st.URL != "" || st.HasToken {
		t.Errorf("a test saved: %+v", st)
	}
}
