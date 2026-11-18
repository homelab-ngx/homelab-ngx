package ask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/backups"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/system"
	"homelab-ngx.com/core/internal/sysupdates"
)

type fakeProvider struct {
	req    llm.Request
	answer []string
}

func (f *fakeProvider) Stream(_ context.Context, r llm.Request, onText func(string)) (llm.AnswerUsage, error) {
	f.req = r
	if r.OnThinking != nil {
		r.OnThinking("Reading the lines.")
	}
	for _, a := range f.answer {
		onText(a)
	}
	return llm.AnswerUsage{OutputTokens: 7}, nil
}

func (f *fakeProvider) Models(context.Context) ([]string, error) { return nil, nil }

type fakeAssistant struct{ p llm.Provider }

func (a fakeAssistant) Provider(context.Context) (llm.Provider, string, error) {
	if a.p == nil {
		return nil, "", llm.ErrOff
	}
	return a.p, "test-model", nil
}

type fakeLogs struct {
	got  system.GetLogsParams
	page system.LogPage
}

func (f *fakeLogs) Logs(_ context.Context, p system.GetLogsParams) (system.LogPage, error) {
	f.got = p
	if p.Unit != "caddy" {
		return system.LogPage{}, httpx.NotFound("unknown_unit", "no unit %q", p.Unit)
	}
	return f.page, nil
}

type fakeBackups struct{}

func (fakeBackups) RunDetail(_ context.Context, run int64) (backups.BackupRunDetail, error) {
	if run != 4 {
		return backups.BackupRunDetail{}, backups.ErrUnknownRun
	}
	id := int64(2)
	return backups.BackupRunDetail{
		BackupRun: backups.BackupRun{Id: 4, BackupId: &id, BackupName: "photos", Result: "failed", Error: "one folder failed"},
		Items:     []backups.BackupRunItem{{Kind: "folder", Destination: "b2", Label: "Library", Path: "/pool/photos", Result: "failed", Error: "permission denied"}},
	}, nil
}

func (fakeBackups) Logs(context.Context, int64) (string, error) {
	return "restic: \x1b[31mFatal\x1b[0m: unable to open repository\n", nil
}

type fakeStacks struct{}

func (fakeStacks) UpdateAttempt(_ context.Context, id int64) (stacks.StackUpdate, error) {
	return stacks.StackUpdate{Id: id, Stack: "immich", Kind: "update", Result: "rolled_back", Reason: "immich-server did not become healthy",
		Log: strings.Repeat("building…\n", 500) + "error: health check timed out\n"}, nil
}

type fakeDeploys struct{}

func (fakeDeploys) Deploys(context.Context) []sysupdates.CoreDeploy {
	return []sysupdates.CoreDeploy{{Id: 99, Kind: "deploy", Result: "rolled_back", Reason: "Caddy did not come back", FailedUnits: []string{"caddy.service"}}}
}

type fakeNotifications struct{}

func (fakeNotifications) Log(_ context.Context, before int64, limit int) ([]notifications.Entry, error) {
	if before != 13 || limit != 1 {
		return []notifications.Entry{{ID: before - 2}}, nil // an older entry: the asked one is gone
	}
	return []notifications.Entry{{ID: 12, Kind: "opened", Severity: "alert", Title: "Disk sdb is failing",
		Body: "SMART reports 8 reallocated sectors.", Deliveries: []notifications.Delivery{{Channel: "email", Error: "smtp: timeout"}}}}, nil
}

func newService(p llm.Provider) (*Service, *fakeLogs) {
	logs := &fakeLogs{page: system.LogPage{More: true, Entries: []system.LogEntry{
		{Message: "newest: listening on :443", Priority: 6, Time: time.Unix(200, 0)},
		{Message: "oldest: \x1b[1mTLS handshake error\x1b[0m", Priority: 3, Time: time.Unix(100, 0)},
	}}}
	return New(fakeAssistant{p: p}, Sources{
		Logs: logs, Backups: fakeBackups{}, Stacks: fakeStacks{}, Deploys: fakeDeploys{}, Notifications: fakeNotifications{},
	}, nil), logs
}

func code(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestAskWithoutAnAssistantIsRefused(t *testing.T) {
	s, _ := newService(nil)
	if _, err := s.Ask(context.Background(), AskRequest{Kind: KindLogs, Ref: "caddy"}, func(string) {}, nil); code(err) != "assistant_off" {
		t.Fatalf("err = %v", err)
	}
	var none *Service
	if _, err := none.Ask(context.Background(), AskRequest{Kind: KindLogs, Ref: "caddy"}, func(string) {}, nil); code(err) != "assistant_off" {
		t.Fatalf("nil service: %v", err)
	}
}

func TestAskStreamsAndSendsTheGuide(t *testing.T) {
	f := &fakeProvider{answer: []string{"Caddy ", "is fine. "}}
	s, _ := newService(f)
	var streamed, thought strings.Builder
	a, err := s.Ask(context.Background(), AskRequest{Kind: KindLogs, Ref: "caddy"}, func(t string) { streamed.WriteString(t) },
		func(t string) { thought.WriteString(t) })
	if err != nil {
		t.Fatal(err)
	}
	if a.Notes != "Caddy is fine." || a.Model != "test-model" || a.Usage.OutputTokens != 7 || streamed.String() != "Caddy is fine. " {
		t.Fatalf("answer = %+v, streamed %q", a, streamed.String())
	}
	if thought.String() != "Reading the lines." || strings.Contains(a.Notes, "Reading") {
		t.Fatalf("thinking %q, answer %q", thought.String(), a.Notes)
	}
	if f.req.System != guide || !strings.HasPrefix(f.req.User, "Question: "+defaults[KindLogs]) {
		t.Fatalf("request = %+v", f.req)
	}
}

func TestAskReadsTheAnswersParts(t *testing.T) {
	s, _ := newService(&fakeProvider{answer: []string{"<summary>Caddy failed to renew.</summary>\n<cause>The DNS challenge timed out", "</cause>\n<steps>\n- Restart Caddy\n- Check the DNS token\n</steps>"}})
	a, err := s.Ask(context.Background(), AskRequest{Kind: KindLogs, Ref: "caddy"}, func(string) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Summary != "Caddy failed to renew." || a.Cause != "The DNS challenge timed out" || len(a.Steps) != 2 || a.Steps[1] != "Check the DNS token" || a.Notes != "" {
		t.Fatalf("answer = %+v", a)
	}
}

func TestLogsPromptIsOldestFirstWithTheFilter(t *testing.T) {
	s, logs := newService(nil)
	p, err := s.Prompt(context.Background(), AskRequest{Kind: KindLogs, Ref: "caddy", Q: "tls", Priority: 4, Question: "  Is TLS broken?  "})
	if err != nil {
		t.Fatal(err)
	}
	if logs.got.Q != "tls" || logs.got.Priority != 4 || logs.got.Lines != logLines {
		t.Fatalf("read %+v", logs.got)
	}
	for _, want := range []string{"Question: Is TLS broken?\n", `only warning and worse, only lines containing "tls"`, "[older lines left out]", "error oldest: TLS handshake error"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Index(p, "oldest:") > strings.Index(p, "newest:") {
		t.Errorf("lines are not oldest first:\n%s", p)
	}
	if strings.Contains(p, "\x1b") {
		t.Errorf("colour codes left in:\n%q", p)
	}
}

func TestServicePromptIgnoresTheFilter(t *testing.T) {
	s, logs := newService(nil)
	p, err := s.Prompt(context.Background(), AskRequest{Kind: KindService, Ref: "caddy", Q: "tls", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	if logs.got.Q != "" || logs.got.Priority != 0 || !strings.Contains(p, "has failed") || !strings.Contains(p, defaults[KindService]) {
		t.Fatalf("read %+v:\n%s", logs.got, p)
	}
	if _, err := s.Prompt(context.Background(), AskRequest{Kind: KindService, Ref: "nope"}); code(err) != "unknown_unit" {
		t.Fatalf("unknown unit: %v", err)
	}
}

func TestRecordPrompts(t *testing.T) {
	s, _ := newService(nil)
	ctx := context.Background()
	cases := []struct {
		in   AskRequest
		want []string
	}{
		{AskRequest{Kind: KindBackupRun, Ref: "4"}, []string{`Backup "photos", run 4`, "Error: one folder failed", "Library (/pool/photos): failed; error: permission denied", "restic: Fatal: unable"}},
		{AskRequest{Kind: KindStackUpdate, Ref: "8"}, []string{`Stack "immich", update attempt 8`, "Reason: immich-server did not become healthy", "[earlier lines left out]", "error: health check timed out"}},
		{AskRequest{Kind: KindCoreDeploy, Ref: "99"}, []string{"Core deploy", "Reason: Caddy did not come back", "during the switch: caddy.service"}},
		{AskRequest{Kind: KindNotification, Ref: "12"}, []string{"a problem was opened", "Title: Disk sdb is failing", "8 reallocated sectors", "Delivery by email failed: smtp: timeout"}},
	}
	for _, c := range cases {
		p, err := s.Prompt(ctx, c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in.Kind, err)
		}
		for _, w := range c.want {
			if !strings.Contains(p, w) {
				t.Errorf("%s prompt lacks %q:\n%s", c.in.Kind, w, p)
			}
		}
	}
	if p, _ := s.Prompt(ctx, AskRequest{Kind: KindStackUpdate, Ref: "8"}); strings.Count(p, "building…") > recordLines {
		t.Errorf("the log is not capped: %d lines", strings.Count(p, "building…"))
	}
}

func TestBadRequests(t *testing.T) {
	s, _ := newService(nil)
	ctx := context.Background()
	for _, c := range []struct {
		in   AskRequest
		code string
	}{
		{AskRequest{Kind: "weather", Ref: "1"}, "invalid_kind"},
		{AskRequest{Kind: KindBackupRun, Ref: "x"}, "invalid_ref"},
		{AskRequest{Kind: KindBackupRun, Ref: "5"}, "unknown_run"},
		{AskRequest{Kind: KindCoreDeploy, Ref: "1"}, "unknown_deploy"},
		{AskRequest{Kind: KindNotification, Ref: "40"}, "unknown_notification"},
	} {
		if _, err := s.Prompt(ctx, c.in); code(err) != c.code {
			t.Errorf("%+v: %v", c.in, err)
		}
	}
	off := New(nil, Sources{}, nil)
	if _, err := off.Prompt(ctx, AskRequest{Kind: KindLogs, Ref: "caddy"}); code(err) != "source_off" {
		t.Errorf("no source: %v", err)
	}
}

func TestAskStreamsOverHTTP(t *testing.T) {
	s, _ := newService(&fakeProvider{answer: []string{"Restart it."}})
	mux := http.NewServeMux()
	NewHTTP(s).Register(httpx.APIs{Operator: httpx.NewAPI(mux, "test", "1")})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(AskRequest{Kind: KindCoreDeploy, Ref: "99"})
	res, err := http.Post(srv.URL+"/api/ask", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", res.StatusCode, out)
	}
	thinking := strings.Index(string(out), "event: thinking\ndata: {\"text\":\"Reading the lines.\"}")
	text := strings.Index(string(out), "event: text\ndata: {\"text\":\"Restart it.\"}")
	answer := strings.Index(string(out), "event: answer\ndata: {")
	if thinking < 0 || text < thinking || answer < text || !strings.Contains(string(out), `"text":"Restart it."`) {
		t.Fatalf("stream:\n%s", out)
	}

	res, err = http.Post(srv.URL+"/api/ask", "application/json", strings.NewReader(`{"kind":"core_deploy","ref":"1"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(out), "event: error\ndata: {\"message\":\"no core deploy 1\"}") {
		t.Fatalf("error stream:\n%s", out)
	}
}
