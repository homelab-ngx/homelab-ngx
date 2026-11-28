package notifications

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"homelab-ngx.com/core/internal/store"
)

const metaReadUpTo = "notifications.read_up_to"

type dbStore struct{ s *store.Store }

// NewDBStore backs a Store with the SQLite runtime store
// (notification_problems, notifications, notification_deliveries and meta).
func NewDBStore(s *store.Store) Store { return dbStore{s} }

func (d dbStore) Problems(ctx context.Context) ([]ProblemState, error) {
	rows, err := d.s.Queries().ListNotificationProblems(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProblemState, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProblemState{
			Key: r.Key, Title: r.Title, Count: int(r.Count),
			OpenedAt: time.UnixMilli(r.OpenedAt), LastSentAt: time.UnixMilli(r.LastSentAt),
		})
	}
	return out, nil
}

func (d dbStore) Commit(ctx context.Context, c Change) ([]Entry, error) {
	var committed []Entry
	err := d.s.Tx(ctx, func(q *store.Queries) error {
		committed = make([]Entry, 0, len(c.Entries))
		for _, p := range c.Upsert {
			if err := q.UpsertNotificationProblem(ctx, store.UpsertNotificationProblemParams{
				Key: p.Key, Title: p.Title, OpenedAt: p.OpenedAt.UnixMilli(),
				LastSentAt: p.LastSentAt.UnixMilli(), Count: int64(p.Count),
			}); err != nil {
				return err
			}
		}
		for _, k := range c.Delete {
			if err := q.DeleteNotificationProblem(ctx, k); err != nil {
				return err
			}
		}
		for _, e := range c.Entries {
			var key *string
			if e.Key != "" {
				k := e.Key
				key = &k
			}
			id, err := q.InsertNotification(ctx, store.InsertNotificationParams{
				At: e.At.UnixMilli(), Key: key, Kind: e.Kind, Event: e.Event, Path: e.Path,
				Severity: string(e.Severity), Title: e.Title, Body: e.Body,
			})
			if err != nil {
				return err
			}
			e.ID = id
			if e.Deliveries == nil {
				e.Deliveries = []Delivery{}
			}
			for _, dl := range e.Deliveries {
				if err := q.InsertNotificationDelivery(ctx, store.InsertNotificationDeliveryParams{
					NotificationID: id, At: dl.At.UnixMilli(), Channel: dl.Channel,
					Ok: dl.OK, Filtered: dl.Filtered, Error: dl.Error,
				}); err != nil {
					return err
				}
			}
			committed = append(committed, e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return committed, nil
}

func (d dbStore) Log(ctx context.Context, before int64, limit int) ([]Entry, error) {
	q := d.s.Queries()
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := q.ListNotificationsBefore(ctx, store.ListNotificationsBeforeParams{ID: before, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	byID := make(map[int64]*Entry, len(rows))
	ids := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = Entry{
			ID: r.ID, At: time.UnixMilli(r.At), Kind: r.Kind, Event: r.Event, Path: r.Path,
			Severity: Severity(r.Severity), Title: r.Title, Body: r.Body, Deliveries: []Delivery{},
		}
		if r.Key != nil {
			out[i].Key = *r.Key
		}
		byID[r.ID] = &out[i]
		ids[i] = r.ID
	}
	dls, err := q.ListNotificationDeliveries(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, dl := range dls {
		e := byID[dl.NotificationID]
		e.Deliveries = append(e.Deliveries, Delivery{
			At: time.UnixMilli(dl.At), Channel: dl.Channel, OK: dl.Ok, Filtered: dl.Filtered, Error: dl.Error,
		})
	}
	return out, nil
}

func (d dbStore) ReadUpTo(ctx context.Context) (int64, error) {
	v, ok, err := d.s.Meta(ctx, metaReadUpTo)
	if err != nil || !ok || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, nil
	}
	return n, nil
}

func (d dbStore) SetReadUpTo(ctx context.Context, id int64) (int64, error) {
	var cur int64
	err := d.s.Tx(ctx, func(q *store.Queries) error {
		raw, err := q.GetMeta(ctx, metaReadUpTo)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && raw != "" {
			if n, perr := strconv.ParseInt(raw, 10, 64); perr == nil {
				cur = n
			}
		}
		if id > cur {
			cur = id
			return q.SetMeta(ctx, store.SetMetaParams{Key: metaReadUpTo, Value: strconv.FormatInt(cur, 10)})
		}
		return nil
	})
	return cur, err
}

func (d dbStore) CountUnread(ctx context.Context, readUpTo int64) (int, error) {
	n, err := d.s.Queries().CountUnreadNotifications(ctx, readUpTo)
	return int(n), err
}

func (d dbStore) LatestID(ctx context.Context) (int64, error) {
	return d.s.Queries().LatestNotificationID(ctx)
}
