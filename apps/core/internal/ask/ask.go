// Package ask answers the dashboard's "Ask" on one thing it shows: some log
// lines, a backup run, a stack update, a core deploy or a notification. The
// dashboard names the thing; the daemon reads what it holds about it, caps
// it, and asks the configured language model once, with no tools and no
// history. The model never sees a variable value or a secret file, only what
// the dashboard itself shows.
package ask

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/backups"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/system"
	"homelab-ngx.com/core/internal/sysupdates"
)

//go:embed guide.md
var guide string

// Kind is what a question is about.
type Kind string

const (
	KindLogs         Kind = "logs"
	KindService      Kind = "service"
	KindBackupRun    Kind = "backup_run"
	KindStackUpdate  Kind = "stack_update"
	KindCoreDeploy   Kind = "core_deploy"
	KindNotification Kind = "notification"
)

// AskRequest is a question about one thing the dashboard shows.
type AskRequest struct {
	Kind Kind   `json:"kind" enum:"logs,service,backup_run,stack_update,core_deploy,notification" doc:"logs: a unit's newest lines under the filter; service: a unit that failed; backup_run, stack_update, core_deploy, notification: one record."`
	Ref  string `json:"ref" minLength:"1" maxLength:"300" doc:"A log viewer unit id (logs, service), or the record's id."`
	// Filter of a logs question, as the log viewer shows it.
	Q        string `json:"q,omitempty" maxLength:"300" doc:"logs: the text the lines must contain."`
	Priority int    `json:"priority,omitempty" minimum:"0" maximum:"7" doc:"logs: the highest syslog priority shown."`
	Question string `json:"question,omitempty" maxLength:"2000" doc:"The question; empty asks what the kind's default question asks."`
}

// AskAnswer is the whole answer, once it has streamed.
type AskAnswer struct {
	Model   string          `json:"model"`
	Usage   llm.AnswerUsage `json:"usage" doc:"What the answer cost, as far as the server says."`
	Summary string          `json:"summary" doc:"What happened, or the answer, in a sentence or two."`
	Cause   string          `json:"cause" doc:"The likely cause and the line that shows it; empty when there is none."`
	Steps   []string        `json:"steps" doc:"What to do, one step each."`
	Notes   string          `json:"notes" doc:"Anything it said outside those parts; the whole answer from a model that ignored them."`
}

// Assistant gives the configured provider; llm.ErrOff when there is none.
type Assistant interface {
	Provider(ctx context.Context) (llm.Provider, string, error)
}

// The sources a question reads. Any may be nil; its kind is then refused.
type (
	LogReader interface {
		Logs(ctx context.Context, p system.GetLogsParams) (system.LogPage, error)
	}
	BackupRuns interface {
		RunDetail(ctx context.Context, run int64) (backups.BackupRunDetail, error)
		Logs(ctx context.Context, backup int64) (string, error)
	}
	StackAttempts interface {
		UpdateAttempt(ctx context.Context, id int64) (stacks.StackUpdate, error)
	}
	CoreDeploys interface {
		Deploys(ctx context.Context) []sysupdates.CoreDeploy
	}
	NotificationLog interface {
		Log(ctx context.Context, before int64, limit int) ([]notifications.Entry, error)
	}
)

// Sources are where each kind's material comes from.
type Sources struct {
	Logs          LogReader
	Backups       BackupRuns
	Stacks        StackAttempts
	Deploys       CoreDeploys
	Notifications NotificationLog
}

// Service answers questions.
type Service struct {
	assistant Assistant
	src       Sources
	log       *slog.Logger
}

// New builds the service.
func New(a Assistant, src Sources, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{assistant: a, src: src, log: log}
}

// Caps on what one question carries. The newest material is kept.
const (
	logLines     = 300 // logs and service questions
	backupLines  = 120 // the backup unit's journal under a run
	recordLines  = 200 // a stack update's or deploy's log
	lineChars    = 600 // one line, cut
	contextChars = 60_000
	timeout      = 10 * time.Minute
)

// defaults is each kind's question when none is given.
var defaults = map[Kind]string{
	KindLogs:         "What do these log lines show? Point out errors and warnings that matter, their likely cause, and what to do about them. If nothing looks wrong, say so.",
	KindService:      "This service failed. Why, and what should I do?",
	KindBackupRun:    "Why did this backup run fail, and what should I do?",
	KindStackUpdate:  "Why did this stack update not end healthy, and what should I do?",
	KindCoreDeploy:   "Why did this deploy of the host fail or roll back, and what should I do?",
	KindNotification: "What does this notification mean, and what should I do about it?",
}

var errOff = httpx.Conflict("assistant_off", "no assistant is set up; set one up in Settings")

// Ask reads the material, asks the assistant and streams the answer: send
// gets each piece of text, think each piece of reasoning a server sends
// (nil: none), and the whole answer is returned.
func (s *Service) Ask(ctx context.Context, in AskRequest, send, think func(string)) (AskAnswer, error) {
	if s == nil || s.assistant == nil {
		return AskAnswer{}, errOff
	}
	p, model, err := s.assistant.Provider(ctx)
	if errors.Is(err, llm.ErrOff) {
		return AskAnswer{}, errOff
	}
	if err != nil {
		return AskAnswer{}, err
	}
	user, err := s.Prompt(ctx, in)
	if err != nil {
		return AskAnswer{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var answer strings.Builder
	timer := llm.StartTimer()
	req := llm.Request{System: guide, User: user, Session: "homelab-ask-" + string(in.Kind), OnThinking: func(t string) {
		timer.Thinking(t)
		if think != nil {
			think(t)
		}
	}}
	usage, err := p.Stream(ctx, req, func(t string) {
		timer.Text(t)
		answer.WriteString(t)
		send(t)
	})
	s.log.Info("assistant answered", append([]any{"ask", in.Kind, "ref", in.Ref, "model", model, "prompt_chars", len(user),
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens, "reasoning_tokens", usage.ReasoningTokens,
		"cost", usage.Cost, "error", err}, timer.LogArgs()...)...)
	if err != nil {
		return AskAnswer{}, httpx.BadGateway("assistant_failed", "%v", err)
	}
	parts, rest := llm.Parts(answer.String(), "summary", "cause", "steps")
	return AskAnswer{Model: model, Usage: usage, Summary: parts["summary"], Cause: parts["cause"],
		Steps: llm.Items(parts["steps"]), Notes: rest}, nil
}

// Prompt is the user text of a question: the question, then the material.
func (s *Service) Prompt(ctx context.Context, in AskRequest) (string, error) {
	question, ok := defaults[in.Kind]
	if !ok {
		return "", httpx.BadRequest("invalid_kind", "unknown kind %q", in.Kind)
	}
	if q := strings.TrimSpace(in.Question); q != "" {
		question = q
	}
	material, err := s.material(ctx, in)
	if err != nil {
		return "", err
	}
	if len(material) > contextChars {
		material = "[earlier material cut]\n" + material[len(material)-contextChars:]
	}
	return fmt.Sprintf("Question: %s\n\n%s", question, material), nil
}

func (s *Service) material(ctx context.Context, in AskRequest) (string, error) {
	switch in.Kind {
	case KindLogs, KindService:
		return s.logs(ctx, in)
	case KindBackupRun:
		return s.backupRun(ctx, in.Ref)
	case KindStackUpdate:
		return s.stackUpdate(ctx, in.Ref)
	case KindCoreDeploy:
		return s.coreDeploy(ctx, in.Ref)
	case KindNotification:
		return s.notification(ctx, in.Ref)
	}
	return "", httpx.BadRequest("invalid_kind", "unknown kind %q", in.Kind)
}

func unavailable(k Kind) error {
	return httpx.Unavailable("source_off", "%s questions are not available on this host", k)
}

func id(ref string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64)
	if err != nil || n <= 0 {
		return 0, httpx.BadRequest("invalid_ref", "ref %q is not a record id", ref)
	}
	return n, nil
}

func (s *Service) logs(ctx context.Context, in AskRequest) (string, error) {
	if s.src.Logs == nil {
		return "", unavailable(in.Kind)
	}
	p := system.GetLogsParams{Unit: in.Ref, Lines: logLines}
	if in.Kind == KindLogs {
		p.Q, p.Priority = in.Q, in.Priority
	}
	page, err := s.src.Logs.Logs(ctx, p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if in.Kind == KindService {
		fmt.Fprintf(&b, "The service with log unit %q has failed. Its newest journal lines, oldest first:\n", in.Ref)
	} else {
		fmt.Fprintf(&b, "Newest journal lines of the unit %q, oldest first", in.Ref)
		var filters []string
		if p.Priority > 0 {
			filters = append(filters, "only "+priorityWord(p.Priority)+" and worse")
		}
		if p.Q != "" {
			filters = append(filters, fmt.Sprintf("only lines containing %q", p.Q))
		}
		if len(filters) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(filters, ", "))
		}
		b.WriteString(":\n")
	}
	if len(page.Entries) == 0 {
		b.WriteString("(no lines)\n")
		return b.String(), nil
	}
	if page.More {
		b.WriteString("[older lines left out]\n")
	}
	entries := slices.Clone(page.Entries)
	slices.Reverse(entries)
	for _, e := range entries {
		fmt.Fprintf(&b, "%s %s %s\n", e.Time.Local().Format("2006-01-02 15:04:05"), priorityWord(e.Priority), cut(clean(e.Message)))
	}
	return b.String(), nil
}

func (s *Service) backupRun(ctx context.Context, ref string) (string, error) {
	if s.src.Backups == nil {
		return "", unavailable(KindBackupRun)
	}
	n, err := id(ref)
	if err != nil {
		return "", err
	}
	r, err := s.src.Backups.RunDetail(ctx, n)
	if errors.Is(err, backups.ErrUnknownRun) {
		return "", httpx.NotFound("unknown_run", "no backup run %d", n)
	}
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Backup %q, run %d (%s), started %s", r.BackupName, r.Id, r.Trigger, stamp(r.StartedAt))
	if r.FinishedAt != nil {
		fmt.Fprintf(&b, ", finished %s", stamp(*r.FinishedAt))
	}
	fmt.Fprintf(&b, ".\nResult: %s\n", r.Result)
	if r.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", r.Error)
	}
	if len(r.Items) > 0 {
		b.WriteString("\nResults per destination and folder:\n")
		for _, it := range r.Items {
			fmt.Fprintf(&b, "- %s on %s", it.Kind, it.Destination)
			if it.Label != "" || it.Path != "" {
				fmt.Fprintf(&b, ", %s (%s)", it.Label, it.Path)
			}
			fmt.Fprintf(&b, ": %s", it.Result)
			if it.Error != "" {
				fmt.Fprintf(&b, "; error: %s", it.Error)
			}
			if it.Note != "" {
				fmt.Fprintf(&b, "; note: %s", it.Note)
			}
			b.WriteString("\n")
		}
	}
	if r.BackupId != nil {
		if lines, err := s.src.Backups.Logs(ctx, *r.BackupId); err == nil && strings.TrimSpace(lines) != "" {
			b.WriteString("\nNewest journal lines of the backup's unit (they may include later runs):\n")
			b.WriteString(tail(lines, backupLines))
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (s *Service) stackUpdate(ctx context.Context, ref string) (string, error) {
	if s.src.Stacks == nil {
		return "", unavailable(KindStackUpdate)
	}
	n, err := id(ref)
	if err != nil {
		return "", err
	}
	u, err := s.src.Stacks.UpdateAttempt(ctx, n)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Stack %q, %s attempt %d, started %s", u.Stack, u.Kind, u.Id, stamp(u.StartedAt))
	if u.Rev != "" {
		fmt.Fprintf(&b, ", to commit %s", short(u.Rev))
	}
	fmt.Fprintf(&b, ".\nGeneration %d to %d.\nResult: %s\n", u.FromGeneration, u.ToGeneration, u.Result)
	if u.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", u.Reason)
	}
	if len(u.Changes) > 0 {
		names := make([]string, 0, min(len(u.Changes), 30))
		for _, c := range u.Changes[:min(len(u.Changes), 30)] {
			names = append(names, c.Name)
		}
		if len(u.Changes) > 30 {
			names = append(names, "…")
		}
		fmt.Fprintf(&b, "\nPackages changed (%d): %s\n", len(u.Changes), strings.Join(names, ", "))
	}
	if strings.TrimSpace(u.Log) != "" {
		b.WriteString("\nIts log (phases and build output, newest lines):\n")
		b.WriteString(tail(u.Log, recordLines))
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (s *Service) coreDeploy(ctx context.Context, ref string) (string, error) {
	if s.src.Deploys == nil {
		return "", unavailable(KindCoreDeploy)
	}
	n, err := id(ref)
	if err != nil {
		return "", err
	}
	list := s.src.Deploys.Deploys(ctx)
	i := slices.IndexFunc(list, func(d sysupdates.CoreDeploy) bool { return d.Id == n })
	if i < 0 {
		return "", httpx.NotFound("unknown_deploy", "no core deploy %d", n)
	}
	d := list[i]
	var b strings.Builder
	fmt.Fprintf(&b, "Core %s started %s", d.Kind, stamp(d.StartedAt))
	if d.Rev != "" {
		fmt.Fprintf(&b, ", of commit %s", short(d.Rev))
	}
	fmt.Fprintf(&b, ".\nLast phase: %s\nResult: %s\n", d.Phase, d.Result)
	if d.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", d.Reason)
	}
	if len(d.FailedUnits) > 0 {
		fmt.Fprintf(&b, "Units that failed during the switch: %s\n", strings.Join(d.FailedUnits, ", "))
	}
	if strings.TrimSpace(d.Log) != "" {
		b.WriteString("\nThe end of the build and switch output:\n")
		b.WriteString(tail(d.Log, recordLines))
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (s *Service) notification(ctx context.Context, ref string) (string, error) {
	if s.src.Notifications == nil {
		return "", unavailable(KindNotification)
	}
	n, err := id(ref)
	if err != nil {
		return "", err
	}
	got, err := s.src.Notifications.Log(ctx, n+1, 1)
	if err != nil {
		return "", err
	}
	if len(got) == 0 || got[0].ID != n {
		return "", httpx.NotFound("unknown_notification", "no notification %d", n)
	}
	e := got[0]
	var b strings.Builder
	fmt.Fprintf(&b, "Notification sent %s.\nKind: %s (%s)\nSeverity: %s\nTitle: %s\n",
		stamp(e.At), e.Kind, kindWord(e.Kind), e.Severity, e.Title)
	if strings.TrimSpace(e.Body) != "" {
		fmt.Fprintf(&b, "Body:\n%s\n", cut(clean(e.Body)))
	}
	for _, d := range e.Deliveries {
		if !d.OK {
			fmt.Fprintf(&b, "Delivery by %s failed: %s\n", d.Channel, d.Error)
		}
	}
	return b.String(), nil
}

func kindWord(k string) string {
	switch k {
	case "opened":
		return "a problem was opened"
	case "reminded":
		return "a reminder that a problem is still open"
	case "resolved":
		return "a problem was resolved"
	}
	return "a one-off message"
}

func priorityWord(p int) string {
	switch {
	case p <= 2:
		return "critical"
	case p == 3:
		return "error"
	case p == 4:
		return "warning"
	case p == 5:
		return "notice"
	case p == 6:
		return "info"
	}
	return "debug"
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// clean drops colour codes and carriage returns.
func clean(s string) string {
	return strings.ReplaceAll(ansiRE.ReplaceAllString(s, ""), "\r", "")
}

// cut shortens one line or body to lineChars, or a body to ten times that.
func cut(s string) string {
	limit := lineChars
	if strings.Contains(s, "\n") {
		limit *= 10
	}
	if len(s) <= limit {
		return s
	}
	return s[:limit] + " […]"
}

// tail is the last n lines of s, cleaned and each cut.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(clean(s), "\n"), "\n")
	var out []string
	if len(lines) > n {
		out = append(out, "[earlier lines left out]")
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		out = append(out, cut(l))
	}
	return strings.Join(out, "\n")
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

func stamp(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }
