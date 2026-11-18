package notifications

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"homelab-ngx.com/core/internal/httpx"
)

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
// is operator.
func (h *HTTP) Register(a httpx.APIs) {
	tags := []string{"notifications"}
	httpx.Register(a, httpx.ScopeRead, huma.Operation{
		OperationID: "getNotifications", Method: http.MethodGet, Path: "/notifications", Tags: tags,
		Summary: "Open problems, failing channels and recent deliveries.",
	}, h.getNotifications)
	httpx.Register(a, httpx.ScopeOperator, huma.Operation{
		OperationID: "applyNotification", Method: http.MethodPost, Path: "/notifications", Tags: tags,
		Summary: "Send a notification, or report/resolve a deduplicated problem.",
		Description: "Single write entry point for scripts and units (`homelab notify`). " +
			"`502` when every channel failed to deliver.",
		Errors: []int{http.StatusBadRequest, http.StatusBadGateway},
	}, h.applyNotification)
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

func (h *HTTP) listNotificationLog(ctx context.Context, in *logIn) (*logOut, error) {
	entries, err := h.n.Log(ctx, in.Before, in.Limit)
	if err != nil {
		return nil, err
	}
	out := make([]NotificationLogEntry, 0, len(entries))
	for _, e := range entries {
		le := NotificationLogEntry{
			Id: e.ID, At: e.At, Kind: NotificationLogEntryKind(e.Kind), Severity: e.Severity,
			Title: e.Title, Body: e.Body, Deliveries: make([]NotificationDelivery, 0, len(e.Deliveries)),
		}
		if e.Key != "" {
			k := e.Key
			le.Key = &k
		}
		for _, d := range e.Deliveries {
			le.Deliveries = append(le.Deliveries, NotificationDelivery{At: d.At, Channel: d.Channel, Ok: d.OK, Error: d.Error})
		}
		out = append(out, le)
	}
	return &logOut{Body: out}, nil
}
