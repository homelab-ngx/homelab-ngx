package notifications

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Problem is one thing currently wrong. Key must be stable across runs
// (e.g. "audit:disk", "unit:podman-foo.service").
type Problem struct {
	Key    string
	Title  string
	Detail string
	Path   string // dashboard path to open
	Event  string // stable dotted event id
}

// Result describes what a reconcile did.
type Result struct {
	Opened   []string `json:"opened"`
	Reminded []string `json:"reminded"`
	Resolved []string `json:"resolved"`
	// Via is the channels that carried the opened or reminded message;
	// empty when none was sent or delivery failed.
	Via string `json:"-"`
}

// Report opens or updates problems without resolving any others. Use it for
// event-driven sources that learn about one problem at a time.
func (n *Notifier) Report(ctx context.Context, p Problem) (Result, error) {
	return n.reconcile(ctx, "", []Problem{p}, false)
}

// Resolve closes one problem, pushing a single "resolved" note if it was open.
func (n *Notifier) Resolve(ctx context.Context, key string) (Result, error) {
	return n.reconcile(ctx, key, nil, true)
}

// Reconcile sets the full set of problems under scope (a key prefix). New
// ones push an alert, long-open ones a single reminder, and ones that are no
// longer present push one "resolved" note. Nothing is pushed otherwise.
func (n *Notifier) Reconcile(ctx context.Context, scope string, current []Problem) (Result, error) {
	return n.reconcile(ctx, scope, current, true)
}

func (n *Notifier) reconcile(ctx context.Context, scope string, current []Problem, resolveMissing bool) (Result, error) {
	var res Result
	n.mu.Lock()
	defer n.mu.Unlock()

	rows, err := n.st.Problems(ctx)
	if err != nil {
		return res, fmt.Errorf("read notification state: %w", err)
	}
	open := make(map[string]ProblemState, len(rows))
	for _, r := range rows {
		open[r.Key] = r
	}

	now := n.now()
	seen := map[string]bool{}
	var fresh, reminders []Problem
	var ch Change
	for _, p := range current {
		seen[p.Key] = true
		st, isOpen := open[p.Key]
		switch {
		case !isOpen:
			fresh = append(fresh, p)
		case now.Sub(st.LastSentAt) >= n.cfg.ReminderAfter:
			reminders = append(reminders, p)
		default:
			st.Count++
			ch.Upsert = append(ch.Upsert, st)
		}
	}
	var gone []string
	if resolveMissing {
		for key := range open {
			if strings.HasPrefix(key, scope) && !seen[key] {
				gone = append(gone, key)
			}
		}
		sort.Strings(gone)
	}

	var firstErr error
	if len(fresh)+len(reminders) > 0 {
		all := append(append([]Problem{}, fresh...), reminders...)
		m := n.alertMessage(fresh, reminders)
		m.Path = commonPath(all)
		m.Event = commonEvent(all)
		if len(all) == 1 {
			m.Key = all[0].Key // a grouped message carries no tag
		}
		via, dels, err := n.deliver(ctx, m, true)
		if errors.Is(err, ErrNoChannels) {
			err = nil
		}
		kind, key := KindReminded, ""
		if len(fresh) > 0 {
			kind = KindOpened
		}
		if len(all) == 1 {
			key = all[0].Key
		}
		ch.Entries = append(ch.Entries, n.entry(kind, key, m, dels))
		if err != nil {
			// Leave the problems unrecorded so the next run retries.
			firstErr = err
		} else {
			res.Via = via
			for _, p := range fresh {
				ch.Upsert = append(ch.Upsert, ProblemState{Key: p.Key, Title: p.Title, OpenedAt: now, LastSentAt: now, Count: 1})
				res.Opened = append(res.Opened, p.Key)
			}
			for _, p := range reminders {
				st := open[p.Key]
				st.LastSentAt, st.Count = now, st.Count+1
				ch.Upsert = append(ch.Upsert, st)
				res.Reminded = append(res.Reminded, p.Key)
			}
		}
	}
	if len(gone) > 0 {
		var titles []string
		for _, k := range gone {
			titles = append(titles, open[k].Title)
		}
		m := Message{Severity: Info, Title: fmt.Sprintf("Resolved: %s", strings.Join(titles, ", ")), Path: "/"}
		if len(gone) == 1 {
			m.Key = gone[0] // a channel may replace the alert with it
		}
		_, dels, err := n.deliver(ctx, m, true)
		if errors.Is(err, ErrNoChannels) {
			err = nil
		}
		key := ""
		if len(gone) == 1 {
			key = gone[0]
		}
		ch.Entries = append(ch.Entries, n.entry(KindResolved, key, m, dels))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			ch.Delete = gone
			res.Resolved = gone
		}
	}
	if firstErr != nil {
		log.Error("notification delivery failed", "error", firstErr.Error())
	}
	if len(ch.Upsert)+len(ch.Delete)+len(ch.Entries) > 0 {
		committed, err := n.st.Commit(ctx, ch)
		if err != nil {
			return res, fmt.Errorf("record notification state: %w", err)
		}
		n.broadcast(ctx, committed)
	}
	return res, nil
}

func (n *Notifier) alertMessage(fresh, reminders []Problem) Message {
	m := Message{Severity: Alert}
	total := len(fresh) + len(reminders)
	switch {
	case total == 1 && len(fresh) == 1:
		m.Title = fresh[0].Title
	case total == 1:
		m.Title = "🔁 " + reminders[0].Title
	case len(reminders) == 0:
		m.Title = fmt.Sprintf("🔴 %d problems", total)
	default:
		m.Title = fmt.Sprintf("🔴 %d problems · %d new", total, len(fresh))
	}
	var lines []string
	for _, p := range fresh {
		lines = append(lines, line(p))
	}
	for _, p := range reminders {
		lines = append(lines, "🔁 "+line(p))
	}
	m.Body = strings.Join(lines, "\n")
	return m
}

func line(p Problem) string {
	if p.Detail == "" {
		return p.Title
	}
	return p.Title + " · " + p.Detail
}

// commonPath returns the shared dashboard path, or "/" when problems differ.
func commonPath(ps []Problem) string {
	path := ps[0].Path
	for _, p := range ps[1:] {
		if p.Path != path {
			return "/"
		}
	}
	if path == "" {
		return "/"
	}
	return path
}

// commonEvent returns the shared event identifier, or "" when problems differ.
func commonEvent(ps []Problem) string {
	ev := ps[0].Event
	for _, p := range ps[1:] {
		if p.Event != ev {
			return ""
		}
	}
	return ev
}
