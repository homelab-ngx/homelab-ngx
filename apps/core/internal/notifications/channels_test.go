package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

// keyFile is a fresh host age key.
func keyFile(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "keys.txt")
	if err := os.WriteFile(p, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newChannels(t *testing.T) (*Channels, *store.Store) {
	t.Helper()
	s, _ := storetest.Open(t)
	return NewChannels(s, keyFile(t)), s
}

// The API never returns a token, the stored secret is sealed, an empty
// token on edit keeps it and clear_token removes it.
func TestChannelTokenStaysSealed(t *testing.T) {
	t.Parallel()
	ch, s := newChannels(t)
	ctx := context.Background()
	c, err := ch.Create(ctx, ChannelInput{Name: "Phone", Kind: KindNtfy, Topic: "homelab-x", Token: "tk_secret"})
	if err != nil || !c.HasToken || c.Server != DefaultNtfyServer || !c.Enabled {
		t.Fatalf("create: %+v %v", c, err)
	}
	row, _ := s.Queries().GetNotificationChannel(ctx, c.ID)
	if strings.Contains(string(row.Secret), "tk_secret") || strings.Contains(row.Config, "tk_secret") {
		t.Fatal("the token is stored in the clear")
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "tk_secret") {
		t.Fatal("the token is in the API view")
	}
	c, err = ch.Update(ctx, c.ID, ChannelInput{Name: "Phone", Topic: "homelab-y"})
	if err != nil || !c.HasToken || c.Topic != "homelab-y" {
		t.Fatalf("an edit without a token must keep it: %+v %v", c, err)
	}
	if c, _ = ch.Update(ctx, c.ID, ChannelInput{Name: "Phone", Topic: "homelab-y", ClearToken: true}); c.HasToken {
		t.Fatal("clear_token kept the token")
	}
}

func TestChannelValidation(t *testing.T) {
	t.Parallel()
	ch, _ := newChannels(t)
	ctx := context.Background()
	for _, in := range []ChannelInput{
		{Name: "", Kind: KindNtfy, Topic: "t"},
		{Name: "x", Kind: "email"},
		{Name: "x", Kind: KindNtfy, Topic: "has space"},
		{Name: "x", Kind: KindNtfy, Topic: "t", Server: "ftp://x"},
		{Name: "x", Kind: KindWebhook, URL: "not a url"},
		{Name: "x", Kind: KindWebhook, URL: "https://user:pw@example.com/"},
		{Name: "x", Kind: KindWebhook, URL: "http://example.com/", CertSHA256: strings.Repeat("a", 64)},
		{Name: "x", Kind: KindWebhook, URL: "https://example.com/", CertSHA256: "abc"},
	} {
		if _, err := ch.Create(ctx, in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	if _, err := ch.Create(ctx, ChannelInput{Name: "x", Kind: KindNtfy, Topic: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Create(ctx, ChannelInput{Name: "x", Kind: KindNtfy, Topic: "u"}); err != ErrChannelExists {
		t.Fatalf("duplicate name: %v", err)
	}
}

// Every enabled channel gets the message; one failing does not fail it, and
// the log has one delivery per channel.
func TestDeliveryToEveryEnabledChannel(t *testing.T) {
	t.Parallel()
	ch, s := newChannels(t)
	ctx := context.Background()
	good, gotGood := newHook(t, 200)
	bad, _ := newHook(t, 500)
	off, gotOff := newHook(t, 200)
	no := false
	for _, in := range []ChannelInput{
		{Name: "good", Kind: KindWebhook, URL: good.URL, Token: testToken},
		{Name: "bad", Kind: KindWebhook, URL: bad.URL, Token: testToken},
		{Name: "off", Kind: KindWebhook, URL: off.URL, Token: testToken, Enabled: &no},
	} {
		if _, err := ch.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	n := New(Config{Targets: ch}, NewDBStore(s), nil)
	via, err := n.Send(ctx, Message{Severity: Alert, Title: "boom"})
	if err != nil || via != "good" || len(*gotGood) != 1 || len(*gotOff) != 0 {
		t.Fatalf("via=%q err=%v good=%d off=%d", via, err, len(*gotGood), len(*gotOff))
	}
	snap, _ := n.Snapshot(ctx)
	if _, failing := snap.FailingChannels["bad"]; !failing || len(snap.FailingChannels) != 1 {
		t.Fatalf("failing = %+v", snap.FailingChannels)
	}
}

func TestNtfyPayload(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []map[string]any
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		got, auth = append(got, b), append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path != "/" {
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	n := Ntfy{Server: srv.URL, Topic: "homelab", Token: "tk_1"}
	if err := n.Send(ctx, Payload{Title: "Disk", Message: "91 %", Severity: Alert, URL: "https://h/storage"}); err != nil {
		t.Fatal(err)
	}
	if err := (Ntfy{Server: srv.URL + "/", Topic: "homelab"}).Send(ctx, Payload{Title: "Resolved: Disk", Severity: Info}); err != nil {
		t.Fatal(err)
	}
	if g := got[0]; g["topic"] != "homelab" || g["title"] != "Disk" || g["message"] != "91 %" || g["priority"] != 4.0 || g["click"] != "https://h/storage" {
		t.Fatalf("alert = %v", g)
	}
	if g := got[1]; g["message"] != "Resolved: Disk" || g["title"] != nil || g["priority"] != 3.0 {
		t.Fatalf("info = %v", g)
	}
	if auth[0] != "Bearer tk_1" || auth[1] != "" {
		t.Fatalf("auth = %q", auth)
	}
}

// A pinned webhook accepts only the pinned certificate.
func TestWebhookPin(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	pin := sha256Hex(srv.Certificate().Raw)
	p := Payload{Title: "t"}
	if err := (Webhook{URL: srv.URL, HTTP: PinnedHTTP(pin)}).Send(context.Background(), p); err != nil {
		t.Fatalf("pinned: %v", err)
	}
	if err := (Webhook{URL: srv.URL, HTTP: PinnedHTTP(strings.Repeat("0", 64))}).Send(context.Background(), p); err == nil {
		t.Fatal("a wrong pin was accepted")
	}
}

func TestHTTPChannels(t *testing.T) {
	t.Parallel()
	hook, got := newHook(t, 200)
	h := newAPI(t, "")
	var c Channel
	body := `{"name":"Hook","kind":"webhook","url":"` + hook.URL + `","token":"` + testToken + `"}`
	if code, m := do(t, h, "POST", "/api/notification-channels", body, &c); code != http.StatusCreated || !c.HasToken {
		t.Fatalf("create: %d %v %+v", code, m, c)
	}
	if code, m := do(t, h, "POST", "/api/notification-channels", body, nil); code != http.StatusConflict || m["code"] != "channel_exists" {
		t.Fatalf("duplicate: %d %v", code, m)
	}
	if code, m := do(t, h, "POST", "/api/notification-channels", `{"name":"x","kind":"webhook","url":"nope"}`, nil); code != http.StatusBadRequest || m["code"] != "invalid_channel" {
		t.Fatalf("invalid: %d %v", code, m)
	}
	var res ChannelTest
	if do(t, h, "POST", "/api/notification-channels/"+jsonInt(c.ID)+"/test", "", &res); !res.OK || len(*got) != 1 || (*got)[0].title != "Test" {
		t.Fatalf("test: %+v %+v", res, *got)
	}
	var list []Channel
	if do(t, h, "GET", "/api/notification-channels", "", &list); len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if code, _ := do(t, h, "DELETE", "/api/notification-channels/"+jsonInt(c.ID), "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do(t, h, "POST", "/api/notification-channels/"+jsonInt(c.ID)+"/test", "", nil); code != http.StatusNotFound {
		t.Fatalf("test after delete: %d", code)
	}
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// A test message links to the channels' page in Settings.
func TestChannelTestLinksToSettings(t *testing.T) {
	t.Parallel()
	ch, _ := newChannels(t)
	ctx := context.Background()
	hook, got := newHook(t, 200)
	c, err := ch.Create(ctx, ChannelInput{Name: "hub", Kind: KindWebhook, URL: hook.URL, Token: testToken})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := ch.Test(ctx, c.ID, "https://homelab.example/"); err != nil || !res.OK {
		t.Fatalf("test: %+v %v", res, err)
	}
	if len(*got) != 1 || (*got)[0].url != "https://homelab.example/settings/notifications" {
		t.Fatalf("pushed %+v", *got)
	}
}
