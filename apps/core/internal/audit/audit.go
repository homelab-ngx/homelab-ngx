// Package audit runs a security audit of the host with the assistant's
// audit model. The daemon gathers what it knows in five areas, each with its
// own material, asks the model about one area at a time, then asks once more
// for a summary of what the areas found. Every chunk's material is stored
// when the audit starts, so a daemon restart continues with the chunks not
// yet done. The model never sees a secret value: the material leaves them out
// by construction, and a last pass replaces any known one with [secret].
package audit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/store"
)

//go:embed guide.md
var guide string

//go:embed summary.md
var summaryGuide string

// Area is one part of the host an audit asks about on its own.
type Area string

const (
	AreaExposure Area = "exposure"
	AreaAccess   Area = "access"
	AreaStacks   Area = "stacks"
	AreaSecrets  Area = "secrets"
	AreaUpdates  Area = "updates"
	// areaSummary is the last chunk: the summary of the others.
	areaSummary Area = "summary"
)

// areas are the audited areas, in the order they are asked.
var areas = []Area{AreaExposure, AreaAccess, AreaStacks, AreaSecrets, AreaUpdates}

// briefs tell a chunk's model what its area covers.
var briefs = map[Area]string{
	AreaExposure: "Exposure and network: what is reachable, from where. The firewall, what listens, the hostnames published to the internet, the host's Caddy sites, the stacks' routes and ports, the VPN and Tailscale.",
	AreaAccess:   "Access: who and what can operate the host. SSH, login users, sudo, nix trusted users, the dashboard and the daemon API, the stacks' API grants, and where the assistant sends material.",
	AreaStacks:   "Stacks: what each stack may do on the host. Its grants (gpu, containers, egress, storage, api), its variables, grants waiting for approval, and where its source comes from.",
	AreaSecrets:  "Backups: where they go and whether they would survive losing the host.",
	AreaUpdates:  "Updates and health: how current the host and the stacks are, a pending reboot, and open problems that bear on security.",
}

// AreaTitles name the areas for people.
var AreaTitles = map[Area]string{
	AreaExposure: "Exposure", AreaAccess: "Access", AreaStacks: "Stacks", AreaSecrets: "Backups",
	AreaUpdates: "Updates", areaSummary: "Summary",
}

// Runtime limits.
const (
	maxAttempts  = 3
	chunkTimeout = 15 * time.Minute
	chunkTokens  = 32_000
	retryWait    = 20 * time.Second
	keepAudits   = 20
)

// Result is how an audit ended.
type Result string

const (
	ResultRunning Result = "running"
	ResultDone    Result = "done"
	ResultPartial Result = "partial"
	ResultFailed  Result = "failed"
)

// ChunkState is where one chunk is.
type ChunkState string

const (
	ChunkPending ChunkState = "pending"
	ChunkRunning ChunkState = "running"
	ChunkDone    ChunkState = "done"
	ChunkFailed  ChunkState = "failed"
)

// Counts are an audit's findings by severity.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

func (c *Counts) add(fs []Finding) {
	for _, f := range fs {
		switch f.Severity {
		case SeverityCritical:
			c.Critical++
		case SeverityHigh:
			c.High++
		case SeverityMedium:
			c.Medium++
		case SeverityLow:
			c.Low++
		default:
			c.Info++
		}
	}
}

// Progress is how far an audit is.
type Progress struct {
	Done    int    `json:"done" doc:"Chunks finished, done or failed."`
	Total   int    `json:"total" doc:"Chunks in all: the areas and the summary."`
	Percent int    `json:"percent"`
	Current string `json:"current,omitempty" enum:"exposure,access,stacks,secrets,updates,summary" doc:"The chunk being asked now."`
}

// SecurityAuditItem is one audit in the list.
type SecurityAuditItem struct {
	ID         int64      `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Result     Result     `json:"result" enum:"running,done,partial,failed" doc:"partial: some areas could not be audited."`
	Model      string     `json:"model"`
	Posture    Posture    `json:"posture,omitempty" enum:"good,fair,weak,poor"`
	Progress   Progress   `json:"progress"`
	Counts     Counts     `json:"counts"`
	// Usage is what the chunks' answers cost together.
	Usage llm.AnswerUsage `json:"usage" doc:"What the audit's answers cost in all, as far as the server says. An attempt a daemon restart cut off is not counted: the server never reported it."`
}

// AuditChunk is one area of an audit, or its summary.
type AuditChunk struct {
	Area       Area             `json:"area" enum:"exposure,access,stacks,secrets,updates,summary"`
	Title      string           `json:"title"`
	State      ChunkState       `json:"state" enum:"pending,running,done,failed"`
	Attempts   int              `json:"attempts"`
	Findings   []Finding        `json:"findings"`
	Strengths  []string         `json:"strengths"`
	Model      string           `json:"model,omitempty"`
	Usage      *llm.AnswerUsage `json:"usage,omitempty"`
	Error      string           `json:"error,omitempty" doc:"Why the last attempt failed."`
	Raw        string           `json:"raw,omitempty" doc:"The model's answer, when it could not be read."`
	Material   string           `json:"material" doc:"Exactly what the model was given."`
	StartedAt  *time.Time       `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at"`
}

// SecurityAudit is one audit in full.
type SecurityAudit struct {
	SecurityAuditItem
	Summary string       `json:"summary"`
	First   []string     `json:"first" doc:"Finding ids to fix first, in that order."`
	Error   string       `json:"error,omitempty"`
	Chunks  []AuditChunk `json:"chunks"`
}

// Assistant gives the provider with the audit model.
type Assistant interface {
	AuditProvider(ctx context.Context) (llm.Provider, string, error)
}

// Notifier tells the operator an audit is done.
type Notifier interface {
	Send(ctx context.Context, m notifications.Message) (string, error)
}

// Service runs audits.
type Service struct {
	st        *store.Store
	assistant Assistant
	src       Sources
	notify    Notifier
	log       *slog.Logger
	now       func() time.Time
	wait      func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	running bool
	life    context.Context
}

// New builds the service; Resume gives it the daemon's lifetime.
func New(st *store.Store, a Assistant, src Sources, n Notifier, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{st: st, assistant: a, src: src, notify: n, log: log, now: time.Now, wait: sleep, life: context.Background()}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

var errOff = httpx.Conflict("assistant_off", "no assistant is set up; set one up in Settings")

// Start gathers the material and starts an audit in the background.
func (s *Service) Start(ctx context.Context) (SecurityAudit, error) {
	if s == nil || s.assistant == nil {
		return SecurityAudit{}, errOff
	}
	_, model, err := s.assistant.AuditProvider(ctx)
	if errors.Is(err, llm.ErrOff) {
		return SecurityAudit{}, errOff
	}
	if err != nil {
		return SecurityAudit{}, err
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return SecurityAudit{}, httpx.Conflict("audit_running", "an audit is already running")
	}
	s.running = true
	s.mu.Unlock()

	id, err := s.create(ctx, model)
	if err != nil {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		return SecurityAudit{}, err
	}
	s.log.Info("security audit started", "audit", id, "model", model)
	go s.run(id)
	return s.Get(ctx, id)
}

// create gathers every chunk's material and stores the audit.
func (s *Service) create(ctx context.Context, model string) (int64, error) {
	g := s.gather(ctx)
	var id int64
	err := s.st.Tx(ctx, func(q *store.Queries) error {
		var err error
		id, err = q.InsertSecurityAudit(ctx, store.InsertSecurityAuditParams{StartedAt: s.now().UnixMilli(), Model: model})
		if err != nil {
			return err
		}
		for i, a := range areas {
			m := g.overview + "\n# Area: " + AreaTitles[a] + "\n" + briefs[a] + "\n\n" + g.areas[a]
			if err := q.InsertSecurityAuditChunk(ctx, store.InsertSecurityAuditChunkParams{AuditID: id, Area: string(a), Ord: int64(i), Material: m}); err != nil {
				return err
			}
		}
		// The summary's material is written when its turn comes: it is
		// what the areas found.
		if err := q.InsertSecurityAuditChunk(ctx, store.InsertSecurityAuditChunkParams{AuditID: id, Area: string(areaSummary), Ord: int64(len(areas))}); err != nil {
			return err
		}
		return q.PruneSecurityAudits(ctx, keepAudits)
	})
	return id, err
}

// Resume continues the audit a restart cut off, under the daemon's
// lifetime ctx; an older one still marked running is closed as failed.
func (s *Service) Resume(ctx context.Context) {
	s.mu.Lock()
	s.life = ctx
	s.mu.Unlock()
	ids, err := s.st.Queries().RunningSecurityAudits(ctx)
	if err != nil {
		s.log.Warn("security audits could not be read", "error", err)
		return
	}
	for i, id := range ids {
		if i < len(ids)-1 {
			_ = s.st.Queries().FinishSecurityAudit(ctx, store.FinishSecurityAuditParams{
				Now: s.now().UnixMilli(), Result: string(ResultFailed), Error: "a newer audit was started", ID: id})
			continue
		}
		s.mu.Lock()
		if s.running {
			s.mu.Unlock()
			return
		}
		s.running = true
		s.mu.Unlock()
		s.log.Info("security audit continues after a restart", "audit", id)
		go s.run(id)
	}
}

// run asks every chunk not yet done, in order, then closes the audit.
func (s *Service) run(id int64) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	s.mu.Lock()
	life := s.life
	s.mu.Unlock()

	chunks, err := s.st.Queries().ListSecurityAuditChunks(life, id)
	if err != nil {
		s.log.Warn("security audit: chunks could not be read", "audit", id, "error", err)
		return
	}
	for _, c := range chunks {
		if ChunkState(c.State) == ChunkDone || ChunkState(c.State) == ChunkFailed {
			continue
		}
		if !s.chunk(life, id, c) {
			return // the daemon is stopping; the next one continues
		}
	}
	s.finish(life, id)
}

// chunk asks one chunk until it is done or out of attempts. It reports
// false when the daemon is stopping, which leaves the chunk as it is.
func (s *Service) chunk(life context.Context, id int64, c store.SecurityAuditChunk) bool {
	area := Area(c.Area)
	attempts := int(c.Attempts)
	lastErr := c.Error
	for {
		if attempts >= maxAttempts {
			msg := fmt.Sprintf("gave up after %d attempts", attempts)
			if lastErr != "" {
				msg += ": " + lastErr
			}
			s.finishChunk(life, id, area, ChunkFailed, nil, nil, c.Raw, "", nil, msg)
			return true
		}
		if area == areaSummary {
			m, err := s.summaryMaterial(life, id)
			if err != nil {
				s.finishChunk(life, id, area, ChunkFailed, nil, nil, "", "", nil, err.Error())
				return true
			}
			c.Material = m
			if err := s.st.Queries().SetSecurityAuditChunkMaterial(life, store.SetSecurityAuditChunkMaterialParams{Material: m, AuditID: id, Area: c.Area}); err != nil {
				s.log.Warn("security audit: summary material not stored", "audit", id, "error", err)
			}
		}
		attempts++
		if err := s.st.Queries().StartSecurityAuditChunk(life, store.StartSecurityAuditChunkParams{Now: s.now().UnixMilli(), AuditID: id, Area: c.Area}); err != nil {
			s.log.Warn("security audit: chunk not started", "audit", id, "area", area, "error", err)
			return life.Err() == nil
		}
		answer, model, usage, err := s.ask(life, id, area, c.Material)
		if life.Err() != nil {
			return false
		}
		if err == nil {
			var findings []Finding
			var strengths []string
			var summary *summaryAnswer
			if area == areaSummary {
				var sa summaryAnswer
				sa, err = parseSummary(answer, findingIDs(c.Material))
				summary = &sa
			} else {
				findings, strengths, err = parseChunk(area, answer, c.Material)
			}
			if err == nil {
				var out any = findings
				if summary != nil {
					out = summary
				}
				s.finishChunk(life, id, area, ChunkDone, out, strengths, "", model, &usage, "")
				s.log.Info("security audit chunk done", "audit", id, "area", area, "findings", len(findings), "model", model)
				return true
			}
		}
		lastErr = err.Error()
		s.log.Warn("security audit chunk failed", "audit", id, "area", area, "attempt", attempts, "error", err)
		state := ChunkPending
		if attempts >= maxAttempts {
			state = ChunkFailed
			lastErr = fmt.Sprintf("gave up after %d attempts: %s", attempts, lastErr)
		}
		s.finishChunk(life, id, area, state, nil, nil, answer, model, &usage, lastErr)
		if state == ChunkFailed {
			return true
		}
		if s.wait(life, retryWait) != nil {
			return false
		}
	}
}

// ask sends one chunk to the audit model.
func (s *Service) ask(life context.Context, id int64, area Area, material string) (string, string, llm.AnswerUsage, error) {
	p, model, err := s.assistant.AuditProvider(life)
	if err != nil {
		return "", model, llm.AnswerUsage{}, err
	}
	ctx, cancel := context.WithTimeout(life, chunkTimeout)
	defer cancel()
	system := guide
	if area == areaSummary {
		system = summaryGuide
	}
	var answer strings.Builder
	timer := llm.StartTimer()
	usage, err := p.Stream(ctx, llm.Request{
		System: system, User: material, MaxTokens: chunkTokens, Reasoning: true,
		Session:    fmt.Sprintf("homelab-audit-%d", id),
		OnThinking: timer.Thinking,
	}, func(t string) {
		timer.Text(t)
		answer.WriteString(t)
	})
	s.log.Info("assistant answered", append([]any{"audit", id, "area", area, "model", model, "prompt_chars", len(material),
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens, "reasoning_tokens", usage.ReasoningTokens,
		"cost", usage.Cost, "error", err}, timer.LogArgs()...)...)
	return answer.String(), model, usage, err
}

func (s *Service) finishChunk(ctx context.Context, id int64, area Area, state ChunkState, findings any, strengths []string, raw, model string, usage *llm.AnswerUsage, msg string) {
	enc := func(v any) string {
		if v == nil {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	var u any
	if usage != nil {
		u = usage
	}
	var st any
	if strengths != nil {
		st = strengths
	}
	err := s.st.Queries().FinishSecurityAuditChunk(ctx, store.FinishSecurityAuditChunkParams{
		State: string(state), Findings: enc(findings), Strengths: enc(st), Raw: raw, Model: model, Usage: enc(u),
		Error: msg, Now: s.now().UnixMilli(), AuditID: id, Area: string(area),
	})
	if err != nil {
		s.log.Warn("security audit: chunk not stored", "audit", id, "area", area, "error", err)
	}
}

// summaryLine is one finding as the summary pass sees it.
type summaryLine struct {
	ID       string   `json:"id"`
	Area     Area     `json:"area"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Risk     string   `json:"risk"`
	Verified bool     `json:"verified"`
}

// summaryMaterial is what the areas found, for the summary pass.
func (s *Service) summaryMaterial(ctx context.Context, id int64) (string, error) {
	chunks, err := s.st.Queries().ListSecurityAuditChunks(ctx, id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var lines []summaryLine
	var strengths []string
	var missing []string
	overviewDone := false
	for _, c := range chunks {
		if Area(c.Area) == areaSummary {
			continue
		}
		if !overviewDone {
			if i := strings.Index(c.Material, "\n# Area: "); i > 0 {
				b.WriteString(c.Material[:i])
				b.WriteString("\n")
				overviewDone = true
			}
		}
		if ChunkState(c.State) != ChunkDone {
			missing = append(missing, AreaTitles[Area(c.Area)])
			continue
		}
		var fs []Finding
		_ = json.Unmarshal([]byte(c.Findings), &fs)
		for _, f := range fs {
			lines = append(lines, summaryLine{ID: f.ID, Area: f.Area, Severity: f.Severity, Title: f.Title, Risk: f.Risk, Verified: f.Verified})
		}
		var ss []string
		_ = json.Unmarshal([]byte(c.Strengths), &ss)
		strengths = append(strengths, ss...)
	}
	if len(missing) == len(areas) {
		return "", errors.New("no area was audited")
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\nNot audited (the model's answer could not be had): %s\n", strings.Join(missing, ", "))
	}
	fl, _ := json.MarshalIndent(lines, "", "  ")
	sl, _ := json.MarshalIndent(strengths, "", "  ")
	fmt.Fprintf(&b, "\n# Findings\n%s\n\n# Strengths\n%s\n", fl, sl)
	return b.String(), nil
}

// findingIDs are the ids the summary material lists.
func findingIDs(material string) map[string]bool {
	ids := map[string]bool{}
	i := strings.Index(material, "# Findings\n")
	if i < 0 {
		return ids
	}
	rest := material[i+len("# Findings\n"):]
	if j := strings.Index(rest, "\n\n# Strengths"); j >= 0 {
		rest = rest[:j]
	}
	var lines []summaryLine
	if json.Unmarshal([]byte(rest), &lines) == nil {
		for _, l := range lines {
			ids[l.ID] = true
		}
	}
	return ids
}

// finish closes the audit from its chunks and tells the operator.
func (s *Service) finish(ctx context.Context, id int64) {
	a, err := s.Get(ctx, id)
	if err != nil {
		s.log.Warn("security audit: not read to finish", "audit", id, "error", err)
		return
	}
	areasDone := 0
	var summary summaryAnswer
	summaryDone := false
	for _, c := range a.Chunks {
		if c.State != ChunkDone {
			continue
		}
		if c.Area == areaSummary {
			summaryDone = true
			continue
		}
		areasDone++
	}
	if summaryDone {
		if row, err := s.chunkRow(ctx, id, areaSummary); err == nil {
			_ = json.Unmarshal([]byte(row.Findings), &summary)
		}
	}
	result, msg := ResultDone, ""
	switch {
	case areasDone == 0:
		result, msg = ResultFailed, "no area could be audited"
	case areasDone < len(areas) || !summaryDone:
		result = ResultPartial
	}
	first, _ := json.Marshal(summary.First)
	if summary.First == nil {
		first = []byte("[]")
	}
	if err := s.st.Queries().FinishSecurityAudit(ctx, store.FinishSecurityAuditParams{
		Now: s.now().UnixMilli(), Result: string(result), Posture: string(summary.Posture), Summary: summary.Summary,
		First: string(first), Error: msg, ID: id,
	}); err != nil {
		s.log.Warn("security audit: not closed", "audit", id, "error", err)
		return
	}
	s.log.Info("security audit finished", "audit", id, "result", result, "posture", summary.Posture)
	if s.notify == nil {
		return
	}
	c := a.Counts
	title := "Security audit ready"
	body := fmt.Sprintf("%d critical, %d high, %d medium findings.", c.Critical, c.High, c.Medium)
	sev := notifications.Info
	switch {
	case result == ResultFailed:
		title, body = "Security audit failed", msg
	case c.Critical > 0 || c.High > 0:
		sev = notifications.Alert
	}
	if result == ResultPartial {
		body += " Some areas could not be audited."
	}
	if _, err := s.notify.Send(ctx, notifications.Message{
		Severity: sev,
		Title:    title,
		Body:     body,
		Path:     "/settings/security",
		Event:    "security.audit",
		Key:      fmt.Sprintf("security:audit:%d", id),
	}); err != nil && !errors.Is(err, notifications.ErrNoChannels) {
		s.log.Warn("security audit: notification not sent", "error", err)
	}
}

func (s *Service) chunkRow(ctx context.Context, id int64, area Area) (store.SecurityAuditChunk, error) {
	chunks, err := s.st.Queries().ListSecurityAuditChunks(ctx, id)
	if err != nil {
		return store.SecurityAuditChunk{}, err
	}
	for _, c := range chunks {
		if Area(c.Area) == area {
			return c, nil
		}
	}
	return store.SecurityAuditChunk{}, sql.ErrNoRows
}

// List returns the newest audits.
func (s *Service) List(ctx context.Context) ([]SecurityAuditItem, error) {
	rows, err := s.st.Queries().ListSecurityAudits(ctx, keepAudits)
	if err != nil {
		return nil, err
	}
	out := make([]SecurityAuditItem, 0, len(rows))
	for _, r := range rows {
		states, err := s.st.Queries().ListSecurityAuditChunkStates(ctx, r.ID)
		if err != nil {
			return nil, err
		}
		item := itemOf(r)
		for _, c := range states {
			var fs []Finding
			if Area(c.Area) != areaSummary && c.Findings != "" {
				_ = json.Unmarshal([]byte(c.Findings), &fs)
			}
			item.Counts.add(fs)
			item.Progress.step(Area(c.Area), ChunkState(c.State))
			addUsage(&item.Usage, c.Usage)
		}
		item.Progress.finish()
		out = append(out, item)
	}
	return out, nil
}

// Get returns one audit in full.
func (s *Service) Get(ctx context.Context, id int64) (SecurityAudit, error) {
	r, err := s.st.Queries().GetSecurityAudit(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return SecurityAudit{}, httpx.NotFound("unknown_audit", "no security audit %d", id)
	}
	if err != nil {
		return SecurityAudit{}, err
	}
	chunks, err := s.st.Queries().ListSecurityAuditChunks(ctx, id)
	if err != nil {
		return SecurityAudit{}, err
	}
	out := SecurityAudit{SecurityAuditItem: itemOf(r), Summary: r.Summary, Error: r.Error, First: []string{}, Chunks: []AuditChunk{}}
	if r.First != "" {
		_ = json.Unmarshal([]byte(r.First), &out.First)
	}
	for _, c := range chunks {
		ac := AuditChunk{
			Area: Area(c.Area), Title: AreaTitles[Area(c.Area)], State: ChunkState(c.State), Attempts: int(c.Attempts),
			Findings: []Finding{}, Strengths: []string{}, Model: c.Model, Error: c.Error, Material: c.Material,
			StartedAt: stamp(c.StartedAt), FinishedAt: stamp(c.FinishedAt),
		}
		if ac.Area != areaSummary && c.Findings != "" {
			_ = json.Unmarshal([]byte(c.Findings), &ac.Findings)
		}
		if c.Strengths != "" {
			_ = json.Unmarshal([]byte(c.Strengths), &ac.Strengths)
		}
		if c.Usage != "" {
			var u llm.AnswerUsage
			if json.Unmarshal([]byte(c.Usage), &u) == nil {
				ac.Usage = &u
			}
		}
		addUsage(&out.Usage, c.Usage)
		if ac.State != ChunkDone {
			ac.Raw = c.Raw
		}
		out.Counts.add(ac.Findings)
		out.Progress.step(ac.Area, ac.State)
		out.Chunks = append(out.Chunks, ac)
	}
	out.Progress.finish()
	return out, nil
}

// addUsage adds a chunk's stored usage (JSON, ” for none) to a total. A
// dollar figure stays absent until some chunk reports one.
func addUsage(t *llm.AnswerUsage, raw string) {
	if raw == "" {
		return
	}
	var u llm.AnswerUsage
	if json.Unmarshal([]byte(raw), &u) != nil {
		return
	}
	t.InputTokens += u.InputTokens
	t.OutputTokens += u.OutputTokens
	t.ReasoningTokens += u.ReasoningTokens
	plus := func(sum **float64, v *float64) {
		if v == nil {
			return
		}
		n := *v
		if *sum != nil {
			n += **sum
		}
		*sum = &n
	}
	plus(&t.Cost, u.Cost)
	plus(&t.Allowance, u.Allowance)
	if t.Billing == "" {
		t.Billing = u.Billing
	}
}

func itemOf(r store.SecurityAudit) SecurityAuditItem {
	return SecurityAuditItem{
		ID: r.ID, StartedAt: time.UnixMilli(r.StartedAt), FinishedAt: stamp(r.FinishedAt),
		Result: Result(r.Result), Model: r.Model, Posture: Posture(r.Posture),
	}
}

func (p *Progress) step(area Area, state ChunkState) {
	p.Total++
	switch state {
	case ChunkDone, ChunkFailed:
		p.Done++
	case ChunkRunning:
		p.Current = string(area)
	case ChunkPending:
		// A chunk waiting to be tried again after a failed attempt, or
		// the next one: the first pending is the one coming up.
	}
}

func (p *Progress) finish() {
	if p.Total > 0 {
		p.Percent = p.Done * 100 / p.Total
	}
}

func stamp(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms)
	return &t
}
