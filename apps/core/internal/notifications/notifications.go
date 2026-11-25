// Package notifications delivers homelab notifications through the
// channels the operator added (ntfy, webhook); every enabled channel gets
// every message. Problems are deduplicated through the runtime store, so a
// stuck failure pushes once when it opens, once when it clears, and one
// reminder a day while it stays open.
package notifications

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	logging "homelab-ngx.com/core/internal/logging"
)

var log = logging.Logger("notifications")

// maxMessage bounds a message's text, in runes (ntfy takes 4 KiB).
const maxMessage = 2000

// Severity selects how loudly the phone announces a message.
type Severity string

const (
	// Alert is something to act on: time-sensitive.
	Alert Severity = "alert"
	// Info is worth knowing: active.
	Info Severity = "info"
)

// Config configures a Notifier.
type Config struct {
	// Targets are the channels that deliver; nil or none leaves
	// notifications off.
	Targets Targets
	// LinkBase is the dashboard origin that tap-through links start with.
	LinkBase string

	ReminderAfter time.Duration // default 24h
}

// Message is one rendered notification.
type Message struct {
	Severity Severity
	Title    string
	Body     string
	Path     string // dashboard path, e.g. /updates
	// Key is the problem key the message is about, or a sender's stable
	// key; a channel may replace an earlier message with the same key. ""
	// for none.
	Key string
}

// Notifier sends notifications and tracks open problems.
type Notifier struct {
	cfg Config
	st  Store
	now func() time.Time

	// mu serialises the read-modify-write of problem state in reconcile so two
	// concurrent reports of the same new problem cannot both push.
	mu sync.Mutex
}

// New creates a Notifier with defaults filled in. A nil st keeps state in
// memory only (the CLI's --once / no-state semantics); a nil now means
// time.Now.
func New(cfg Config, st Store, now func() time.Time) *Notifier {
	if cfg.ReminderAfter == 0 {
		cfg.ReminderAfter = 24 * time.Hour
	}
	if now == nil {
		now = time.Now
	}
	if st == nil {
		st = newMemStore()
	}
	return &Notifier{cfg: cfg, st: st, now: now}
}

// Enabled reports whether any channel is enabled.
func (n *Notifier) Enabled() bool {
	ts, err := n.targets(context.Background())
	return err == nil && len(ts) > 0
}

func (n *Notifier) targets(ctx context.Context) ([]Target, error) {
	if n.cfg.Targets == nil {
		return nil, nil
	}
	return n.cfg.Targets.Targets(ctx)
}

// Send delivers one message with no dedup state and records it in the log.
// It returns the channel that carried it. A store failure never blocks or
// fails delivery.
func (n *Notifier) Send(ctx context.Context, m Message) (string, error) {
	channel, attempts, err := n.deliver(ctx, m)
	n.record(ctx, Change{Entries: []Entry{n.entry(KindSent, m.Key, m, attempts)}}, len(attempts))
	return channel, err
}

// record commits c unless nothing was attempted. Failures are logged only:
// the notification already went out (or visibly failed).
func (n *Notifier) record(ctx context.Context, c Change, attempts int) {
	if attempts == 0 && len(c.Upsert) == 0 && len(c.Delete) == 0 {
		return
	}
	if err := n.st.Commit(ctx, c); err != nil {
		log.Warn("notification state unavailable", "error", err)
	}
}

func (n *Notifier) entry(kind, key string, m Message, attempts []Delivery) Entry {
	return Entry{At: n.now(), Key: key, Kind: kind, Severity: m.Severity, Title: m.Title, Body: m.Body, Deliveries: attempts}
}

// deliver sends m to every enabled channel and returns the names of those
// that took it, joined, and one attempt per channel. It fails only when no
// channel took it.
func (n *Notifier) deliver(ctx context.Context, m Message) (string, []Delivery, error) {
	targets, err := n.targets(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(targets) == 0 {
		return "", nil, errors.New("no notification channel configured")
	}
	p := n.payload(m)
	var via []string
	var errs []error
	attempts := make([]Delivery, 0, len(targets))
	for _, t := range targets {
		err := t.Sender.Send(ctx, p)
		d := Delivery{At: n.now(), Channel: t.Name, OK: err == nil}
		if err != nil {
			d.Error = err.Error()
			errs = append(errs, errors.New(t.Name+": "+err.Error()))
		} else {
			via = append(via, t.Name)
		}
		attempts = append(attempts, d)
	}
	if len(via) == 0 {
		return "", attempts, errors.Join(errs...)
	}
	return strings.Join(via, ", "), attempts, nil
}

// payload is m as a channel gets it: the dashboard link from the path.
func (n *Notifier) payload(m Message) Payload {
	return Payload{
		Title: truncate(m.Title, maxMessage), Message: truncate(m.Body, maxMessage),
		Severity: m.Severity, Key: m.Key, URL: n.link(m.Path), SentAt: n.now().Unix(),
	}
}

func (n *Notifier) link(path string) string {
	if n.cfg.LinkBase == "" {
		return ""
	}
	if path == "" {
		path = "/"
	}
	return strings.TrimRight(n.cfg.LinkBase, "/") + path
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}
