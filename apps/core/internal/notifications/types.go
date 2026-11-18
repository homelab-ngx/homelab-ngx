package notifications

import (
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// Schema documents Severity as its enum.
func (Severity) Schema(huma.Registry) *huma.Schema {
	return enumSchema(Alert, Info)
}

// NotifyRequest is one notification action: the API's request body and the
// CLI's direct fallback.
type NotifyRequest struct {
	// Action `send` pushes one message; `report` opens or updates a deduplicated
	// problem (needs `key` and `title`); `resolve` closes one (needs `key`).
	Action NotifyRequestAction `json:"action" doc:"send pushes one message; report opens or updates a deduplicated problem (needs key and title); resolve closes one (needs key)."`
	Body   string              `json:"body,omitempty"`

	// Key Stable problem key, e.g. `audit:disk`.
	Key string `json:"key,omitempty" doc:"Stable problem key, e.g. audit:disk."`

	// Path Dashboard path to open from the notification.
	Path     string   `json:"path,omitempty" doc:"Dashboard path to open from the notification."`
	Severity Severity `json:"severity,omitempty"`
	Tag      string   `json:"tag,omitempty"`
	Title    string   `json:"title,omitempty"`
}

// NotifyRequestAction is what a NotifyRequest does.
type NotifyRequestAction string

const (
	Report  NotifyRequestAction = "report"
	Resolve NotifyRequestAction = "resolve"
	Send    NotifyRequestAction = "send"
)

// Schema documents NotifyRequestAction as its enum.
func (NotifyRequestAction) Schema(huma.Registry) *huma.Schema {
	return enumSchema(Send, Report, Resolve)
}

// NotifyResponse says what an action did.
type NotifyResponse struct {
	// Channel Channel that delivered a `send`.
	Channel  string   `json:"channel,omitempty" doc:"Channel that delivered a send."`
	Opened   []string `json:"opened"`
	Reminded []string `json:"reminded"`
	Resolved []string `json:"resolved"`
}

// OpenProblem is one open problem in the snapshot.
type OpenProblem struct {
	Count    int       `json:"count"`
	OpenedAt time.Time `json:"opened_at"`
	Title    string    `json:"title"`
}

// ChannelFailure is a channel whose latest attempts failed.
type ChannelFailure struct {
	Error string    `json:"error"`
	Since time.Time `json:"since"`
}

// NotificationHistoryEntry is one delivery attempt.
type NotificationHistoryEntry struct {
	At time.Time `json:"at"`

	// Channel is the channel's name.
	Channel  string   `json:"channel" doc:"The channel's name."`
	Error    string   `json:"error,omitempty"`
	Ok       bool     `json:"ok"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
}

// NotificationsSnapshot is the notification state the dashboard shows.
type NotificationsSnapshot struct {
	FailingChannels map[string]ChannelFailure  `json:"failing_channels"`
	History         []NotificationHistoryEntry `json:"history"`
	Open            map[string]OpenProblem     `json:"open"`
}

// NotificationDelivery is one delivery attempt of a log entry.
type NotificationDelivery struct {
	At      time.Time `json:"at"`
	Channel string    `json:"channel"`
	Error   string    `json:"error"`
	Ok      bool      `json:"ok"`
}

// NotificationLogEntry is one notification log entry with its deliveries.
type NotificationLogEntry struct {
	At         time.Time              `json:"at"`
	Body       string                 `json:"body"`
	Deliveries []NotificationDelivery `json:"deliveries"`
	Id         int64                  `json:"id"`

	// Key Problem key; null for plain sends.
	Key      *string                  `json:"key" doc:"Problem key; null for plain sends."`
	Kind     NotificationLogEntryKind `json:"kind"`
	Severity Severity                 `json:"severity"`
	Title    string                   `json:"title"`
}

// NotificationLogEntryKind is what a log entry records.
type NotificationLogEntryKind string

const (
	Opened   NotificationLogEntryKind = "opened"
	Reminded NotificationLogEntryKind = "reminded"
	Resolved NotificationLogEntryKind = "resolved"
	Sent     NotificationLogEntryKind = "sent"
)

// Schema documents NotificationLogEntryKind as its enum.
func (NotificationLogEntryKind) Schema(huma.Registry) *huma.Schema {
	return enumSchema(Sent, Opened, Reminded, Resolved)
}

func enumSchema[T ~string](values ...T) *huma.Schema {
	s := &huma.Schema{Type: huma.TypeString}
	for _, v := range values {
		s.Enum = append(s.Enum, string(v))
	}
	return s
}
