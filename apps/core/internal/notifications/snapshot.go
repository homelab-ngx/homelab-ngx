package notifications

import (
	"context"
	"sort"
)

// historyDepth is how many log entries feed the Snapshot's history and the
// failing-channel derivation.
const historyDepth = 200

// Snapshot is the read-only view served to the dashboard. History lists
// delivery attempts oldest first. failing_channels is derived: a channel fails
// when its latest attempt failed, since the first failure of that streak.
func (n *Notifier) Snapshot(ctx context.Context) (NotificationsSnapshot, error) {
	out := NotificationsSnapshot{
		Open:            map[string]OpenProblem{},
		FailingChannels: map[string]ChannelFailure{},
		History:         []NotificationHistoryEntry{},
	}
	problems, err := n.st.Problems(ctx)
	if err != nil {
		return out, err
	}
	for _, p := range problems {
		out.Open[p.Key] = OpenProblem{Title: p.Title, OpenedAt: p.OpenedAt, Count: p.Count}
	}
	entries, err := n.st.Log(ctx, 0, historyDepth)
	if err != nil {
		return out, err
	}
	for i := len(entries) - 1; i >= 0; i-- { // oldest first
		e := entries[i]
		for _, d := range e.Deliveries {
			out.History = append(out.History, NotificationHistoryEntry{
				At: d.At, Channel: d.Channel, Severity: e.Severity, Title: e.Title, Ok: d.OK, Error: d.Error,
			})
		}
	}
	sort.SliceStable(out.History, func(i, j int) bool { return out.History[i].At.Before(out.History[j].At) })
	for _, h := range out.History {
		if h.Ok {
			delete(out.FailingChannels, h.Channel)
		} else if _, failing := out.FailingChannels[h.Channel]; !failing {
			out.FailingChannels[h.Channel] = ChannelFailure{Since: h.At, Error: h.Error}
		}
	}
	return out, nil
}

// Log returns notification log entries, newest first (keyset by ID).
func (n *Notifier) Log(ctx context.Context, before int64, limit int) ([]Entry, error) {
	return n.st.Log(ctx, before, limit)
}

// OpenProblems returns every currently open (deduplicated) problem.
func (n *Notifier) OpenProblems(ctx context.Context) ([]ProblemState, error) {
	return n.st.Problems(ctx)
}
