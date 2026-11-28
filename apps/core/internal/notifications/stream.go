package notifications

import "context"

// StreamEvent carries either a new committed log entry or an updated snapshot
// to live notification stream subscribers.
type StreamEvent struct {
	Entry    *NotificationLogEntry
	Snapshot *NotificationsSnapshot
}

// Subscribe registers an SSE subscriber, returning the initial snapshot,
// an event channel, and a cancel func that removes the subscription.
func (n *Notifier) Subscribe(ctx context.Context) (NotificationsSnapshot, <-chan StreamEvent, func(), error) {
	ch := make(chan StreamEvent, 64)
	n.subMu.Lock()
	if n.subs == nil {
		n.subs = map[chan StreamEvent]struct{}{}
	}
	n.subs[ch] = struct{}{}
	n.subMu.Unlock()

	cancel := func() {
		n.subMu.Lock()
		delete(n.subs, ch)
		n.subMu.Unlock()
	}

	snap, err := n.Snapshot(ctx)
	if err != nil {
		cancel()
		return NotificationsSnapshot{}, nil, nil, err
	}
	return snap, ch, cancel, nil
}

// BroadcastSnapshot pushes the current snapshot to active stream subscribers.
func (n *Notifier) BroadcastSnapshot(ctx context.Context) {
	n.broadcast(ctx, nil)
}

func (n *Notifier) broadcast(ctx context.Context, committed []Entry) {
	n.subMu.Lock()
	hasSubs := len(n.subs) > 0
	n.subMu.Unlock()
	if !hasSubs {
		return
	}

	snap, err := n.Snapshot(ctx)
	if err != nil {
		log.Warn("snapshot broadcast skipped", "error", err)
		return
	}
	events := make([]StreamEvent, 0, len(committed)+1)
	for _, e := range committed {
		le := logEntryOf(e)
		events = append(events, StreamEvent{Entry: &le})
	}
	events = append(events, StreamEvent{Snapshot: &snap})

	n.subMu.Lock()
	for ch := range n.subs {
		for _, ev := range events {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	n.subMu.Unlock()
}

func logEntryOf(e Entry) NotificationLogEntry {
	le := NotificationLogEntry{
		Id: e.ID, At: e.At, Kind: NotificationLogEntryKind(e.Kind),
		Event: e.Event, Path: e.Path, Severity: e.Severity,
		Title: e.Title, Body: e.Body, Deliveries: make([]NotificationDelivery, 0, len(e.Deliveries)),
	}
	if e.Key != "" {
		k := e.Key
		le.Key = &k
	}
	for _, d := range e.Deliveries {
		le.Deliveries = append(le.Deliveries, NotificationDelivery{
			At: d.At, Channel: d.Channel, Ok: d.OK, Filtered: d.Filtered, Error: d.Error,
		})
	}
	return le
}
