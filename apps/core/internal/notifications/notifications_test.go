package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/store/storetest"
)

// push is one notification the fake webhook received.
type push struct {
	title, message, key, url, severity string
}

// newHook is a fake webhook receiver answering status to every POST.
func newHook(t *testing.T, status int) (*httptest.Server, *[]push) {
	t.Helper()
	var mu sync.Mutex
	var got []push
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("unexpected call %s %s", r.Method, r.Header.Get("Authorization"))
		}
		var b Payload
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Errorf("body: %v", err)
		}
		if b.SentAt == 0 {
			t.Error("no sent_at")
		}
		mu.Lock()
		got = append(got, push{b.Title, b.Message, b.Key, b.URL, string(b.Severity)})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

const testToken = "0123456789abcdef0123456789abcdef"

func testNotifier(t *testing.T, hookURL string, now *time.Time) *Notifier {
	t.Helper()
	return testNotifierStore(t, hookURL, now, nil)
}

// fixed is a fixed list of channels.
type fixed []Target

func (f fixed) Targets(context.Context) ([]Target, error) { return f, nil }

// testNotifierStore builds a notifier with one webhook channel "test" at
// hookURL; "" means no channel.
func testNotifierStore(t *testing.T, hookURL string, now *time.Time, st Store) *Notifier {
	t.Helper()
	cfg := Config{LinkBase: "https://homelab.example.com"}
	if hookURL != "" {
		cfg.Targets = fixed{{Name: "test", Sender: Webhook{URL: hookURL, Token: testToken}}}
	}
	return New(cfg, st, func() time.Time { return *now })
}

// eachStore runs fn against every Store implementation (nil = in-memory).
func eachStore(t *testing.T, fn func(t *testing.T, st Store)) {
	t.Run("memory", func(t *testing.T) { t.Parallel(); fn(t, nil) })
	t.Run("db", func(t *testing.T) {
		t.Parallel()
		s, _ := storetest.Open(t)
		fn(t, NewDBStore(s))
	})
}

func TestReconcileDedupReminderResolve(t *testing.T) {
	t.Parallel()
	eachStore(t, testReconcileDedupReminderResolve)
}

func testReconcileDedupReminderResolve(t *testing.T, st Store) {
	srv, got := newHook(t, 200)
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	n := testNotifierStore(t, srv.URL, &now, st)
	ctx := context.Background()
	disk := Problem{Key: "audit:disk", Title: "Disk almost full", Detail: "/pool 95%", Path: "/storage"}

	for i := 0; i < 3; i++ {
		if _, err := n.Reconcile(ctx, "audit:", []Problem{disk}); err != nil {
			t.Fatal(err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("repeated runs pushed %d times, want 1", len(*got))
	}
	p := (*got)[0]
	want := push{title: "Disk almost full", message: "Disk almost full · /pool 95%", key: "audit:disk",
		url: "https://homelab.example.com/storage", severity: "alert"}
	if p != want {
		t.Fatalf("push = %+v, want %+v", p, want)
	}

	now = now.Add(25 * time.Hour)
	if res, _ := n.Reconcile(ctx, "audit:", []Problem{disk}); len(res.Reminded) != 1 {
		t.Fatalf("no reminder after 25h: %+v", res)
	}
	if len(*got) != 2 {
		t.Fatalf("pushes = %d, want 2", len(*got))
	}

	res, _ := n.Reconcile(ctx, "audit:", nil)
	if len(res.Resolved) != 1 || len(*got) != 3 {
		t.Fatalf("resolve: res=%+v pushes=%d", res, len(*got))
	}
	// The resolved note carries the alert's key, so a channel can replace it.
	if last := (*got)[2]; last != (push{title: "Resolved: Disk almost full", key: "audit:disk",
		url: "https://homelab.example.com/", severity: "info"}) {
		t.Fatalf("resolved push = %+v", last)
	}
	if _, _ = n.Reconcile(ctx, "audit:", nil); len(*got) != 3 {
		t.Fatal("resolved problem pushed again")
	}
}

func TestScopeIsolation(t *testing.T) {
	t.Parallel()
	srv, got := newHook(t, 200)
	now := time.Now()
	n := testNotifier(t, srv.URL, &now)
	ctx := context.Background()
	_, _ = n.Report(ctx, Problem{Key: "unit:a", Title: "a failing"})
	// An audit reconcile with no problems must not resolve an unrelated unit.
	_, _ = n.Reconcile(ctx, "audit:", nil)
	if len(*got) != 1 {
		t.Fatalf("pushes = %d, want 1", len(*got))
	}
	res, _ := n.Resolve(ctx, "unit:a")
	if len(res.Resolved) != 1 {
		t.Fatalf("resolve = %+v", res)
	}
}

func TestDeliveryFailureIsRecorded(t *testing.T) {
	t.Parallel()
	eachStore(t, func(t *testing.T, st Store) {
		srv, _ := newHook(t, 503)
		now := time.Now()
		n := testNotifierStore(t, srv.URL, &now, st)
		ch, err := n.Send(context.Background(), Message{Severity: Alert, Title: "boom", Body: "x"})
		if err == nil || ch != "" {
			t.Fatalf("channel=%q err=%v", ch, err)
		}
		snap, _ := n.Snapshot(context.Background())
		if f, ok := snap.FailingChannels["test"]; !ok || !strings.Contains(f.Error, "503") {
			t.Fatalf("the channel is not reported failing: %+v", snap.FailingChannels)
		}
	})
}

// A plain send carries its severity, key and dashboard link; a message
// without a body has an empty message.
func TestSendMapping(t *testing.T) {
	t.Parallel()
	srv, got := newHook(t, 200)
	now := time.Now()
	n := testNotifier(t, srv.URL, &now)
	ctx := context.Background()
	if ch, err := n.Send(ctx, Message{Severity: Info, Title: "Backup finished", Body: "b2-backup ran", Path: "/backups"}); err != nil || ch != "test" {
		t.Fatalf("ch=%q err=%v", ch, err)
	}
	if _, err := n.Apply(ctx, NotifyRequest{Action: "send", Severity: Alert, Key: "update:build", Title: "Build failed"}); err != nil {
		t.Fatal(err)
	}
	want := []push{
		{title: "Backup finished", message: "b2-backup ran", url: "https://homelab.example.com/backups", severity: "info"},
		{title: "Build failed", key: "update:build", url: "https://homelab.example.com/", severity: "alert"},
	}
	if len(*got) != 2 || (*got)[0] != want[0] || (*got)[1] != want[1] {
		t.Fatalf("pushes = %+v, want %+v", *got, want)
	}
}

// Several problems in one message carry no tag: none of them may replace
// the others' notifications.
func TestGroupedMessageHasNoTag(t *testing.T) {
	t.Parallel()
	srv, got := newHook(t, 200)
	now := time.Now()
	n := testNotifier(t, srv.URL, &now)
	_, _ = n.Reconcile(context.Background(), "audit:", []Problem{{Key: "audit:a", Title: "a"}, {Key: "audit:b", Title: "b"}})
	if len(*got) != 1 || (*got)[0].key != "" || (*got)[0].title != "🔴 2 problems" {
		t.Fatalf("pushes = %+v", *got)
	}
}

func TestDisabledWithoutChannels(t *testing.T) {
	t.Parallel()
	now := time.Now()
	n := testNotifier(t, "", &now)
	if n.Enabled() {
		t.Fatal("enabled without a channel")
	}
	if _, err := n.Send(context.Background(), Message{Title: "x"}); err == nil {
		t.Fatal("send without a channel succeeded")
	}
}

func TestFailedDeliveryKeepsProblemRetryable(t *testing.T) {
	t.Parallel()
	srv, got := newHook(t, 500)
	now := time.Now()
	n := testNotifier(t, srv.URL, &now)
	p := Problem{Key: "audit:x", Title: "x"}
	_, _ = n.Reconcile(context.Background(), "audit:", []Problem{p})
	_, _ = n.Reconcile(context.Background(), "audit:", []Problem{p})
	if len(*got) != 2 {
		t.Fatalf("attempts = %d, want a retry each run", len(*got))
	}
	snap, _ := n.Snapshot(context.Background())
	if len(snap.Open) != 0 {
		t.Fatalf("undelivered problem recorded as open: %+v", snap.Open)
	}
}

func TestSnapshotDerivesFailingChannelsAndRecovers(t *testing.T) {
	t.Parallel()
	eachStore(t, func(t *testing.T, st Store) {
		var status atomic.Int32
		status.Store(500)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(status.Load())) }))
		defer srv.Close()
		now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
		n := testNotifierStore(t, srv.URL, &now, st)
		ctx := context.Background()

		_, _ = n.Send(ctx, Message{Severity: Info, Title: "one"})
		first := now
		now = now.Add(time.Minute)
		_, _ = n.Send(ctx, Message{Severity: Info, Title: "two"})
		snap, err := n.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := snap.FailingChannels["test"]
		if !ok || !f.Since.Equal(first) || f.Error == "" {
			t.Fatalf("failing = %+v", snap.FailingChannels)
		}
		if len(snap.History) != 2 || snap.History[0].Title != "one" || snap.History[1].Title != "two" {
			t.Fatalf("history (oldest first) = %+v", snap.History)
		}

		status.Store(200)
		now = now.Add(time.Minute)
		if ch, err := n.Send(ctx, Message{Severity: Info, Title: "three"}); err != nil || ch != "test" {
			t.Fatalf("ch=%q err=%v", ch, err)
		}
		snap, _ = n.Snapshot(ctx)
		if len(snap.FailingChannels) != 0 {
			t.Fatalf("channel should have recovered: %+v", snap.FailingChannels)
		}
		if snap.Open == nil || snap.History == nil {
			t.Fatal("snapshot maps must be non-nil")
		}
	})
}

func TestLogKindsKeysAndPagination(t *testing.T) {
	t.Parallel()
	eachStore(t, func(t *testing.T, st Store) {
		srv, _ := newHook(t, 200)
		now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
		n := testNotifierStore(t, srv.URL, &now, st)
		ctx := context.Background()
		_, _ = n.Send(ctx, Message{Severity: Info, Title: "plain"})
		_, _ = n.Report(ctx, Problem{Key: "unit:a", Title: "a failing"})
		now = now.Add(25 * time.Hour)
		_, _ = n.Report(ctx, Problem{Key: "unit:a", Title: "a failing"})
		_, _ = n.Resolve(ctx, "unit:a")

		all, err := n.Log(ctx, 0, 10)
		if err != nil || len(all) != 4 {
			t.Fatalf("log = %d entries, err %v", len(all), err)
		}
		kinds := []string{all[0].Kind, all[1].Kind, all[2].Kind, all[3].Kind}
		if strings.Join(kinds, ",") != "resolved,reminded,opened,sent" {
			t.Fatalf("kinds (newest first) = %v", kinds)
		}
		if all[3].Key != "" || all[2].Key != "unit:a" || len(all[2].Deliveries) != 1 || all[2].Deliveries[0].Channel != "test" {
			t.Fatalf("entries = %+v", all)
		}
		page, _ := n.Log(ctx, all[1].ID, 10)
		if len(page) != 2 || page[0].Kind != "opened" {
			t.Fatalf("page before %d = %+v", all[1].ID, page)
		}
		if one, _ := n.Log(ctx, 0, 1); len(one) != 1 || one[0].Kind != "resolved" {
			t.Fatalf("limit not applied: %+v", one)
		}
	})
}

func TestDBLogAttachesDeliveriesPerEntry(t *testing.T) {
	t.Parallel()
	s, _ := storetest.Open(t)
	st, ctx := NewDBStore(s), context.Background()
	at := time.UnixMilli(1_760_000_000_000)
	dl := func(ch string, ok bool, errMsg string) Delivery {
		return Delivery{At: at, Channel: ch, OK: ok, Error: errMsg}
	}
	entry := func(title string, dls ...Delivery) Entry {
		return Entry{At: at, Kind: KindSent, Severity: Info, Title: title, Deliveries: dls}
	}
	if err := st.Commit(ctx, Change{Entries: []Entry{
		entry("old", dl("email", true, "")),
		entry("two", dl("ntfy", false, "503"), dl("email", true, "")),
		entry("none"),
		entry("one", dl("ntfy", true, "")),
	}}); err != nil {
		t.Fatal(err)
	}
	summary := func(es []Entry) string {
		var parts []string
		for _, e := range es {
			var chs []string
			for _, d := range e.Deliveries {
				chs = append(chs, fmt.Sprintf("%s:%t:%s@%d", d.Channel, d.OK, d.Error, d.At.UnixMilli()))
			}
			parts = append(parts, e.Title+"["+strings.Join(chs, ",")+"]")
		}
		return strings.Join(parts, " ")
	}

	page, err := st.Log(ctx, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := "one[ntfy:true:@1760000000000] none[] two[ntfy:false:503@1760000000000,email:true:@1760000000000]"
	if got := summary(page); got != want {
		t.Fatalf("page 1 = %s, want %s", got, want)
	}
	if page[1].Deliveries == nil || page[0].ID <= page[1].ID || page[1].ID <= page[2].ID {
		t.Fatalf("want newest-first ids and non-nil empty deliveries: %+v", page)
	}

	older, err := st.Log(ctx, page[2].ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(older); got != "old[email:true:@1760000000000]" {
		t.Fatalf("page before %d = %s", page[2].ID, got)
	}

	empty, err := st.Log(ctx, older[0].ID, 3)
	if err != nil || len(empty) != 0 {
		t.Fatalf("page before oldest = %+v, err %v", empty, err)
	}
}

func TestNilStoreIsInMemory(t *testing.T) {
	t.Parallel()
	srv, got := newHook(t, 200)
	now := time.Now()
	n := testNotifier(t, srv.URL, &now)
	p := Problem{Key: "k", Title: "t"}
	_, _ = n.Report(context.Background(), p)
	_, _ = n.Report(context.Background(), p)
	if len(*got) != 1 {
		t.Fatalf("in-memory dedup pushed %d times", len(*got))
	}
}

func TestApplyInvalid(t *testing.T) {
	t.Parallel()
	now := time.Now()
	n := testNotifier(t, "", &now)
	for _, r := range []NotifyRequest{{Action: "report"}, {Action: "resolve"}, {Action: "send"}, {Action: "bogus"}} {
		if _, err := n.Apply(context.Background(), r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err = %v, want ErrInvalid", r, err)
		}
	}
}

func TestSendPreservesKeyInLog(t *testing.T) {
	t.Parallel()
	srv, _ := newHook(t, 200)
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	n := testNotifier(t, srv.URL, &now)
	ctx := context.Background()
	if _, err := n.Send(ctx, Message{Severity: Alert, Key: "stack:media", Title: "Stack media did not start"}); err != nil {
		t.Fatal(err)
	}
	entries, err := n.Log(ctx, 0, 10)
	if err != nil || len(entries) != 1 || entries[0].Key != "stack:media" {
		t.Fatalf("entries = %+v, err %v", entries, err)
	}
}
