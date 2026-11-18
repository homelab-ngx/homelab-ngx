// Package notifytest points a notifier at a fake webhook receiver in other
// packages' tests.
package notifytest

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"homelab-ngx.com/core/internal/notifications"
)

// Targets is a fixed list of channels.
type Targets []notifications.Target

// Targets returns the list.
func (t Targets) Targets(context.Context) ([]notifications.Target, error) { return t, nil }

// Config is a notifier config delivering to one webhook channel at baseURL
// (an httptest server whose handler reads requests with Title).
func Config(t testing.TB, baseURL string) notifications.Config {
	t.Helper()
	return notifications.Config{Targets: Targets{{Name: "test", Sender: notifications.Webhook{URL: baseURL, Token: "test-token"}}}}
}

// Title is the headline a delivered notification shows: its title, or its
// message when it has none.
func Title(r *http.Request) string {
	var b struct{ Title, Message string }
	_ = json.NewDecoder(r.Body).Decode(&b)
	if b.Title != "" {
		return b.Title
	}
	return b.Message
}
