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

// ErrNoChannels reports that no notification channel is enabled.
var ErrNoChannels = errors.New("no notification channel configured")

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
	// external delivery off while still recording to the tray and log.
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
	Path     string // dashboard path, e.g. /settings/updates
	Event    string // stable dotted event id, e.g. core.update
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

	subMu sync.Mutex
	subs  map[chan StreamEvent]struct{}
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
	return &Notifier{cfg: cfg, st: st, now: now, subs: map[chan StreamEvent]struct{}{}}
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

// Send delivers one message with no dedup state and records it in the log
// and live stream. A missing channel configuration still records the entry
// for the dashboard tray and returns ErrNoChannels.
func (n *Notifier) Send(ctx context.Context, m Message) (string, error) {
	channel, attempts, err := n.deliver(ctx, m, false)
	committed, cerr := n.st.Commit(ctx, Change{Entries: []Entry{n.entry(KindSent, m.Key, m, attempts)}})
	if cerr != nil {
		log.Warn("notification state unavailable", "error", cerr)
	} else {
		n.broadcast(ctx, committed)
	}
	return channel, err
}

// Emit records and delivers an internal daemon event. No configured channels
// is treated as normal; real delivery failures are logged.
func (n *Notifier) Emit(ctx context.Context, m Message) {
	if _, err := n.Send(ctx, m); err != nil && !errors.Is(err, ErrNoChannels) {
		log.Warn("event notification not delivered", "event", m.Event, "title", m.Title, "error", err)
	}
}

func (n *Notifier) entry(kind, key string, m Message, attempts []Delivery) Entry {
	return Entry{
		At: n.now(), Key: key, Kind: kind, Event: m.Event, Path: m.Path,
		Severity: m.Severity, Title: m.Title, Body: m.Body, Deliveries: attempts,
	}
}

// deliver sends m to every enabled channel whose filter permits it and
// records filtered skips. It fails with ErrNoChannels when no channel is
// enabled, or with the joined errors when every attempted channel failed;
// when all enabled channels filtered m out, it returns "", attempts, nil.
func (n *Notifier) deliver(ctx context.Context, m Message, isProblem bool) (string, []Delivery, error) {
	targets, err := n.targets(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(targets) == 0 {
		return "", nil, ErrNoChannels
	}
	p := n.payload(m)
	cat := CategoryFor(m.Event, m.Key)
	var via []string
	var errs []error
	attempted := 0
	attempts := make([]Delivery, 0, len(targets))
	for _, t := range targets {
		if !t.Filter.Allows(cat, m.Severity, isProblem) {
			attempts = append(attempts, Delivery{At: n.now(), Channel: t.Name, Filtered: true})
			continue
		}
		attempted++
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
	if attempted == 0 {
		return "", attempts, nil
	}
	if len(via) == 0 {
		return "", attempts, errors.Join(errs...)
	}
	return strings.Join(via, ", "), attempts, nil
}

// MarkRead advances the read marker to upTo (or the latest entry id when upTo
// is 0), broadcasts an updated snapshot, and returns the resulting counts.
func (n *Notifier) MarkRead(ctx context.Context, upTo int64) (MarkReadResponse, error) {
	latest, err := n.st.LatestID(ctx)
	if err != nil {
		return MarkReadResponse{}, err
	}
	target := upTo
	if target <= 0 || target > latest {
		target = latest
	}
	readUpTo, err := n.st.SetReadUpTo(ctx, target)
	if err != nil {
		return MarkReadResponse{}, err
	}
	unread, err := n.st.CountUnread(ctx, readUpTo)
	if err != nil {
		return MarkReadResponse{}, err
	}
	n.BroadcastSnapshot(ctx)
	return MarkReadResponse{ReadUpTo: readUpTo, Unread: unread}, nil
}

// payload is m as a channel gets it: the dashboard link from the path.
func (n *Notifier) payload(m Message) Payload {
	return Payload{
		Title: truncate(m.Title, maxMessage), Message: truncate(m.Body, maxMessage),
		Severity: m.Severity, Event: m.Event, Key: m.Key, URL: n.link(m.Path), SentAt: n.now().Unix(),
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
