package audit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store"
	"homelab-ngx.com/core/internal/store/storetest"
)

// fakeModel answers each chunk by its area, from answers; an area missing
// from answers gets an empty report.
type fakeModel struct {
	mu      sync.Mutex
	answers map[Area]string
	asked   []Area
	reqs    []llm.Request
	// block, when set, holds every answer until ctx is done.
	block bool
}

func areaOf(user string) Area {
	for _, a := range areas {
		if strings.Contains(user, "# Area: "+AreaTitles[a]+"\n") {
			return a
		}
	}
	return areaSummary
}

func (f *fakeModel) Stream(ctx context.Context, r llm.Request, onText func(string)) (llm.AnswerUsage, error) {
	a := areaOf(r.User)
	f.mu.Lock()
	f.asked = append(f.asked, a)
	f.reqs = append(f.reqs, r)
	ans, ok := f.answers[a]
	block := f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return llm.AnswerUsage{}, ctx.Err()
	}
	if !ok {
		ans = `<report>{"findings":[],"strengths":[]}</report>`
		if a == areaSummary {
			ans = `<report>{"summary":"Fine.","posture":"good","first":[]}</report>`
		}
	}
	onText(ans)
	return llm.AnswerUsage{InputTokens: 10, OutputTokens: 5}, nil
}

func (f *fakeModel) Models(context.Context) ([]string, error) { return nil, nil }

func (f *fakeModel) askedAreas() []Area {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Area(nil), f.asked...)
}

type fakeAssistant struct{ p *fakeModel }

func (a fakeAssistant) AuditProvider(context.Context) (llm.Provider, string, error) {
	if a.p == nil {
		return nil, "", llm.ErrOff
	}
	return a.p, "audit-model", nil
}

type fakeStacks struct{ secret string }

func (f fakeStacks) List(context.Context) ([]stacks.Stack, error) {
	return []stacks.Stack{{
		Name: "media", Source: "https://user:pw@example.com/media.git", Running: true,
		Approved: &stacks.StackRevision{Rev: "abcdef1234567890", Manifest: stacks.StackManifest{
			Name: "media", Gpu: true, Api: []string{"control"},
			Routes: []stacks.StackManifestRoute{{Host: "films", Port: 8096}},
			Ports:  []stacks.StackManifestPort{{Port: 8096, To: 8096, Proto: "tcp", From: []string{"lan"}}},
		}},
		Variables: []stacks.StackVariable{{Name: "API_KEY", Kind: "secret", Set: true}},
	}}, nil
}

func (f fakeStacks) SecretValues(context.Context) ([]string, error) { return []string{f.secret}, nil }

type fakeNotify struct {
	mu   sync.Mutex
	sent []notifications.Message
}

func (f *fakeNotify) Send(_ context.Context, m notifications.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return "", nil
}

const stackSecret = "s3cr3t-stack-value"

func newService(t *testing.T, m *fakeModel) (*Service, *fakeNotify) {
	t.Helper()
	st, _ := storetest.Open(t)
	n := &fakeNotify{}
	src := Sources{
		Host: config.Security{
			Firewall: config.Firewall{Enabled: true, TCPPorts: []int{22, 443}},
			SSH:      config.SSH{Enabled: true, Ports: []int{22}, Settings: map[string]any{"PasswordAuthentication": false}},
			// A careless note that leaks a secret.
			Notes: []string{"the stack key is " + stackSecret},
		},
		Tunnel: config.CloudflareTunnel{Enabled: true, Hostnames: []string{"photos.example.org"}, PublicHostnames: map[string]string{"photos.example.org": "the app cannot log in through Access"}},
		Domain: "home.example",
		Stacks: fakeStacks{secret: stackSecret},
	}
	var a Assistant = fakeAssistant{}
	if m != nil {
		a = fakeAssistant{p: m}
	}
	s := New(st, a, src, n, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.wait = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return s, n
}

// waitDone polls until the audit is no longer running.
func waitDone(t *testing.T, s *Service, id int64) SecurityAudit {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a, err := s.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if a.Result != ResultRunning && !running {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the audit did not finish")
	return SecurityAudit{}
}

func TestAuditAsksEachAreaThenSummarises(t *testing.T) {
	m := &fakeModel{answers: map[Area]string{
		AreaExposure: `Here it is.
<report>
` + "```json" + `
{"findings":[
 {"severity":"low","title":"Low thing","evidence":"open on every interface: TCP 22, 443","risk":"r","fix":"f"},
 {"severity":"HIGH","title":"Public photos","evidence":"photos.example.org:  deliberately public","risk":"r","fix":"f"},
 {"severity":"critical","title":"Made up","evidence":"port 31337 is open to the world","risk":"r","fix":"f"}
],"strengths":["SSH keys only"]}
` + "```" + `
</report>`,
		areaSummary: `<report>{"summary":"Mostly fine.","posture":"weak","first":["exposure-2","nope-9","exposure-1"]}</report>`,
	}}
	s, n := newService(t, m)
	ctx := context.Background()
	started, err := s.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := waitDone(t, s, started.ID)

	if got := m.askedAreas(); fmt.Sprint(got) != fmt.Sprint(append(append([]Area{}, areas...), areaSummary)) {
		t.Fatalf("asked %v", got)
	}
	if a.Result != ResultDone || a.Posture != PostureWeak || a.Summary != "Mostly fine." || a.Model != "audit-model" {
		t.Fatalf("audit = %+v", a.SecurityAuditItem)
	}
	if fmt.Sprint(a.First) != "[exposure-2 exposure-1]" {
		t.Fatalf("first = %v", a.First)
	}
	fs := a.Chunks[0].Findings
	if len(fs) != 3 || fs[0].Title != "Made up" || fs[0].ID != "exposure-1" || fs[1].Severity != SeverityHigh || fs[2].Severity != SeverityLow {
		t.Fatalf("findings = %+v", fs)
	}
	if fs[0].Verified || !fs[1].Verified || !fs[2].Verified {
		t.Fatalf("verified = %v %v %v", fs[0].Verified, fs[1].Verified, fs[2].Verified)
	}
	// Six chunks of 10 in and 5 out each; the fake server reports no dollars.
	if a.Usage.InputTokens != 60 || a.Usage.OutputTokens != 30 || a.Usage.Cost != nil {
		t.Fatalf("usage = %+v", a.Usage)
	}
	if a.Counts != (Counts{Critical: 1, High: 1, Low: 1}) || a.Progress.Percent != 100 {
		t.Fatalf("counts = %+v, progress = %+v", a.Counts, a.Progress)
	}
	// Reasoning is asked for, and nothing secret leaves.
	for _, r := range m.reqs {
		if !r.Reasoning || r.MaxTokens != chunkTokens {
			t.Fatalf("request = %+v", r)
		}
		for _, leak := range []string{stackSecret, "user:pw"} {
			if strings.Contains(r.User, leak) {
				t.Fatalf("%q was sent:\n%s", leak, r.User)
			}
		}
	}
	if !strings.Contains(a.Chunks[0].Material, "[secret]") || !strings.Contains(a.Chunks[0].Material, "media: route https://films.home.example -> its port 8096") {
		t.Fatalf("material:\n%s", a.Chunks[0].Material)
	}
	if len(n.sent) != 1 || n.sent[0].Severity != notifications.Alert || !strings.Contains(n.sent[0].Body, "1 critical, 1 high") {
		t.Fatalf("sent = %+v", n.sent)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Counts.Critical != 1 || list[0].Posture != PostureWeak || list[0].Usage.InputTokens != 60 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestAnAreaThatCannotBeReadFailsAfterThreeAttempts(t *testing.T) {
	m := &fakeModel{answers: map[Area]string{AreaStacks: "I think it is fine."}}
	s, n := newService(t, m)
	started, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := waitDone(t, s, started.ID)
	c := a.Chunks[2]
	if a.Result != ResultPartial || c.State != ChunkFailed || c.Attempts != maxAttempts || c.Raw != "I think it is fine." ||
		!strings.Contains(c.Error, "no <report> part") {
		t.Fatalf("audit = %+v, stacks = %+v", a.SecurityAuditItem, c)
	}
	asked := 0
	for _, x := range m.askedAreas() {
		if x == AreaStacks {
			asked++
		}
	}
	if asked != maxAttempts || a.Chunks[5].State != ChunkDone {
		t.Fatalf("stacks asked %d times; summary %+v", asked, a.Chunks[5])
	}
	if !strings.Contains(n.sent[0].Body, "Some areas could not be audited") {
		t.Fatalf("sent = %+v", n.sent)
	}
}

func TestAResumedAuditSkipsTheChunksDone(t *testing.T) {
	m := &fakeModel{}
	s, _ := newService(t, m)
	ctx := context.Background()
	id, err := s.create(ctx, "audit-model")
	if err != nil {
		t.Fatal(err)
	}
	q := s.st.Queries()
	for _, a := range []Area{AreaExposure, AreaAccess} {
		if err := q.FinishSecurityAuditChunk(ctx, store.FinishSecurityAuditChunkParams{State: "done", Findings: "[]", Strengths: "[]", AuditID: id, Area: string(a)}); err != nil {
			t.Fatal(err)
		}
	}
	// The stacks chunk was being asked when the daemon stopped.
	if err := q.StartSecurityAuditChunk(ctx, store.StartSecurityAuditChunkParams{Now: 1, AuditID: id, Area: string(AreaStacks)}); err != nil {
		t.Fatal(err)
	}
	s.Resume(ctx)
	a := waitDone(t, s, id)
	if got := fmt.Sprint(m.askedAreas()); got != "[stacks secrets updates summary]" {
		t.Fatalf("asked %s", got)
	}
	if a.Result != ResultDone || a.Chunks[2].Attempts != 2 {
		t.Fatalf("audit = %+v, stacks = %+v", a.SecurityAuditItem, a.Chunks[2])
	}
}

func TestAStoppingDaemonLeavesTheChunkToResume(t *testing.T) {
	m := &fakeModel{block: true}
	s, n := newService(t, m)
	life, stop := context.WithCancel(context.Background())
	s.Resume(life)
	started, err := s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(context.Background()); !isProblem(err, "audit_running") {
		t.Fatalf("second start: %v", err)
	}
	for len(m.askedAreas()) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	for {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	a, err := s.Get(context.Background(), started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Result != ResultRunning || a.Chunks[0].State != ChunkRunning || a.Chunks[0].Attempts != 1 || a.Progress.Current != string(AreaExposure) {
		t.Fatalf("audit = %+v, chunk = %+v", a.SecurityAuditItem, a.Chunks[0])
	}
	if len(n.sent) != 0 {
		t.Fatalf("sent = %+v", n.sent)
	}
}

func TestStartWithoutAnAssistant(t *testing.T) {
	s, _ := newService(t, nil)
	if _, err := s.Start(context.Background()); !isProblem(err, "assistant_off") {
		t.Fatalf("err = %v", err)
	}
}

func isProblem(err error, code string) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Code == code
}

func TestParseSS(t *testing.T) {
	out := `tcp   LISTEN 0      4096         0.0.0.0:22        0.0.0.0:*    users:(("sshd",pid=1201,fd=3))
tcp   LISTEN 0      4096            [::]:22           [::]:*    users:(("sshd",pid=1201,fd=4))
udp   UNCONN 0      0            0.0.0.0:51820     0.0.0.0:*
tcp   LISTEN 0      4096         0.0.0.0:22        0.0.0.0:*    users:(("sshd",pid=1202,fd=3))
`
	want := "tcp 0.0.0.0:22 sshd\ntcp [::]:22 sshd\nudp 0.0.0.0:51820\n"
	if got := parseSS(out); got != want {
		t.Fatalf("got %q", got)
	}
}

func TestFirewallRulesDropTheBoilerplate(t *testing.T) {
	in := `# Helper command
ip46tables -w -t nat -D PREROUTING -j nixos-nat-pre 2>/dev/null|| true
# SMB
iptables -A nixos-fw -p tcp -s 192.168.1.0/24 --dport 445 -j nixos-fw-accept
ip46tables -w -t nat -N nixos-nat-pre
iptables -w -t filter -A nixos-filter-forward \
  -i 'wg-rw' -o eth0 -j ACCEPT
`
	got := firewallRules(in)
	if len(got) != 2 || !strings.Contains(got[0], "--dport 445") || !strings.Contains(got[1], "wg-rw") {
		t.Fatalf("got %q", got)
	}
}

func TestParseChunkRefusesAnUnknownSeverity(t *testing.T) {
	_, _, err := parseChunk(AreaAccess, `<report>{"findings":[{"severity":"spicy","title":"x"}]}</report>`, "")
	if err == nil || !strings.Contains(err.Error(), "spicy") {
		t.Fatalf("err = %v", err)
	}
	many := `<report>{"findings":[` + strings.Repeat(`{"severity":"info","title":"x"},`, 14) + `{"severity":"high","title":"y"}]}</report>`
	fs, _, err := parseChunk(AreaAccess, many, "")
	if err != nil || len(fs) != maxFindings || fs[0].Title != "y" || fs[9].ID != "access-10" {
		t.Fatalf("fs = %+v, %v", fs, err)
	}
	if _, err := parseSummary(`<report>{"summary":"s","posture":"great"}</report>`, nil); err == nil {
		t.Fatal("posture great was accepted")
	}
}

func TestAddUsageSumsDollarsOnlyWhenReported(t *testing.T) {
	var total llm.AnswerUsage
	addUsage(&total, `{"input_tokens":100,"output_tokens":50,"allowance":0.01,"billing":"go"}`)
	addUsage(&total, `{"input_tokens":20,"output_tokens":10,"allowance":0.02}`)
	addUsage(&total, "")
	if total.InputTokens != 120 || total.OutputTokens != 60 || total.Cost != nil || total.Allowance == nil ||
		fmt.Sprintf("%.2f", *total.Allowance) != "0.03" || total.Billing != "go" {
		t.Fatalf("total = %+v", total)
	}
}
