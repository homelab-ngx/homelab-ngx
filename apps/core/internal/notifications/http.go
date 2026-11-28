package notifications

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"
	"homelab-ngx.com/core/internal/httpx"
)

const streamHeartbeat = 25 * time.Second

// HTTP is the notifications slice of the API.
type HTTP struct {
	n  *Notifier
	ch *Channels
}

// NewHTTP returns the HTTP adapter for n and its channels ch.
func NewHTTP(n *Notifier, ch *Channels) *HTTP { return &HTTP{n: n, ch: ch} }

type snapshotOut struct{ Body NotificationsSnapshot }
type applyIn struct{ Body NotifyRequest }
type applyOut struct{ Body NotifyResponse }
type markReadIn struct{ Body MarkReadRequest }
type markReadOut struct{ Body MarkReadResponse }
type logIn struct {
	Before int64 `query:"before" minimum:"1" doc:"Return only entries with an id lower than this (keyset pagination)."`
	Limit  int   `query:"limit" minimum:"1" maximum:"500" default:"50"`
}
type logOut struct{ Body []NotificationLogEntry }
type channelsOut struct{ Body []Channel }
type channelOut struct{ Body Channel }
type channelIn struct{ Body ChannelInput }
type channelIDIn struct {
	ID int64 `path:"id"`
}
type channelUpdateIn struct {
	ID   int64 `path:"id"`
	Body ChannelInput
}
type channelTestOut struct{ Body ChannelTest }

// Register adds the notifications operations: the reads are read, sending
// and read-state writes are operator.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"notifications"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getNotifications", Method: http.MethodGet, Path: "/notifications", Tags: tags,
		Summary: "Open problems, failing channels, unread count and recent deliveries.",
	}, h.getNotifications)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "applyNotification", Method: http.MethodPost, Path: "/notifications", Tags: tags,
		Summary: "Send a notification, or report/resolve a deduplicated problem.",
		Description: "Single write entry point for scripts and units (`homelab notify`). " +
			"`502` when every attempted channel failed to deliver.",
		Errors: []int{http.StatusBadRequest, http.StatusBadGateway},
	}, h.applyNotification)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "markNotificationsRead", Method: http.MethodPost, Path: "/notifications/read", Tags: tags,
		Summary: "Mark notification history read through an entry id.",
	}, h.markNotificationsRead)
	httpx.RegisterSSE(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "streamNotifications", Method: http.MethodGet, Path: "/notifications/stream", Tags: tags,
		Summary: "Live notification events: an initial snapshot, then new entries and updated snapshots.",
	}, map[string]any{
		"snapshot": NotificationsSnapshot{},
		"entry":    NotificationLogEntry{},
		"ping":     NotificationPing{},
	}, h.streamNotifications)
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "listNotificationLog", Method: http.MethodGet, Path: "/notifications/log", Tags: tags,
		Summary: "Notification history with delivery attempts (newest first).",
	}, h.listNotificationLog)

	chTags := []string{"notifications"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "listNotificationChannels", Method: http.MethodGet, Path: "/notification-channels", Tags: chTags,
		Summary: "The channels notifications go to; no tokens.",
	}, h.listChannels)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "createNotificationChannel", Method: http.MethodPost, Path: "/notification-channels", Tags: chTags,
		Summary: "Add a channel: ntfy or a webhook.", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusBadRequest, http.StatusConflict},
	}, h.createChannel)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "updateNotificationChannel", Method: http.MethodPut, Path: "/notification-channels/{id}", Tags: chTags,
		Summary: "Change a channel; an empty token keeps the stored one.",
		Errors:  []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict},
	}, h.updateChannel)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "deleteNotificationChannel", Method: http.MethodDelete, Path: "/notification-channels/{id}", Tags: chTags,
		Summary: "Remove a channel.", DefaultStatus: http.StatusNoContent, Errors: []int{http.StatusNotFound},
	}, h.deleteChannel)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "testNotificationChannel", Method: http.MethodPost, Path: "/notification-channels/{id}/test", Tags: chTags,
		Summary:     "Send a test notification through one channel.",
		Description: "`200` with `ok: false` and the reason when the channel refused or could not be reached.",
		Errors:      []int{http.StatusNotFound},
	}, h.testChannel)
}

// channelError maps the channel errors onto problems.
func channelError(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return httpx.BadRequest("invalid_channel", "%s", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	case errors.Is(err, ErrChannelNotFound):
		return httpx.NotFound("channel_not_found", "%s", err)
	case errors.Is(err, ErrChannelExists):
		return httpx.Conflict("channel_exists", "%s", err)
	}
	return err
}

func (h *HTTP) listChannels(ctx context.Context, _ *struct{}) (*channelsOut, error) {
	cs, err := h.ch.List(ctx)
	if err != nil {
		return nil, err
	}
	return &channelsOut{Body: cs}, nil
}

func (h *HTTP) createChannel(ctx context.Context, in *channelIn) (*channelOut, error) {
	c, err := h.ch.Create(ctx, in.Body)
	if err != nil {
		return nil, channelError(err)
	}
	return &channelOut{Body: c}, nil
}

func (h *HTTP) updateChannel(ctx context.Context, in *channelUpdateIn) (*channelOut, error) {
	c, err := h.ch.Update(ctx, in.ID, in.Body)
	if err != nil {
		return nil, channelError(err)
	}
	return &channelOut{Body: c}, nil
}

func (h *HTTP) deleteChannel(ctx context.Context, in *channelIDIn) (*struct{}, error) {
	if err := h.ch.Delete(ctx, in.ID); err != nil {
		return nil, channelError(err)
	}
	return nil, nil
}

func (h *HTTP) testChannel(ctx context.Context, in *channelIDIn) (*channelTestOut, error) {
	t, err := h.ch.Test(ctx, in.ID, h.n.cfg.LinkBase)
	if err != nil {
		return nil, channelError(err)
	}
	return &channelTestOut{Body: t}, nil
}

func (h *HTTP) getNotifications(ctx context.Context, _ *struct{}) (*snapshotOut, error) {
	snap, err := h.n.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return &snapshotOut{Body: snap}, nil
}

// applyNotification sends, reports or resolves: 400 for a malformed request,
// 502 when every channel failed to deliver.
func (h *HTTP) applyNotification(ctx context.Context, in *applyIn) (*applyOut, error) {
	resp, err := h.n.Apply(ctx, in.Body)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			return nil, httpx.BadRequest("invalid_notification", "%s", err)
		}
		return nil, httpx.BadGateway("delivery_failed", "%s", err)
	}
	return &applyOut{Body: resp}, nil
}

func (h *HTTP) markNotificationsRead(ctx context.Context, in *markReadIn) (*markReadOut, error) {
	res, err := h.n.MarkRead(ctx, in.Body.UpTo)
	if err != nil {
		return nil, err
	}
	return &markReadOut{Body: res}, nil
}

func (h *HTTP) streamNotifications(ctx context.Context, _ *struct{}, send sse.Sender) {
	snap, ch, cancel, err := h.n.Subscribe(ctx)
	if err != nil {
		return
	}
	defer cancel()
	if send.Data(snap) != nil {
		return
	}
	hb := time.NewTicker(streamHeartbeat)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Entry != nil && send.Data(*ev.Entry) != nil {
				return
			}
			if ev.Snapshot != nil && send.Data(*ev.Snapshot) != nil {
				return
			}
		case now := <-hb.C:
			if send.Data(NotificationPing{Time: now}) != nil {
				return
			}
		}
	}
}

func (h *HTTP) listNotificationLog(ctx context.Context, in *logIn) (*logOut, error) {
	entries, err := h.n.Log(ctx, in.Before, in.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]NotificationLogEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, logEntryOf(e))
	}
	return &logOut{Body: out}, nil
}
