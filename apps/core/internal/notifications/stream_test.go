package notifications

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sseMsg struct {
	event string
	data  string
}

func readSSE(r *bufio.Reader, timeout time.Duration) (sseMsg, bool) {
	ch := make(chan sseMsg, 1)
	go func() {
		var msg sseMsg
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if msg.data != "" {
					ch <- msg
					return
				}
				continue
			}
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				msg.event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				msg.data = v
			}
		}
	}()
	select {
	case m := <-ch:
		return m, true
	case <-time.After(timeout):
		return sseMsg{}, false
	}
}

func TestStreamAndMarkReadHTTP(t *testing.T) {
	t.Parallel()
	hook, _ := newHook(t, 200)
	h := newAPI(t, hook.URL)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/notifications/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %v err=%v", resp, err)
	}
	defer resp.Body.Close()

	rd := bufio.NewReader(resp.Body)
	first, ok := readSSE(rd, 2*time.Second)
	if !ok || first.event != "snapshot" {
		t.Fatalf("initial event = %+v ok=%v", first, ok)
	}
	var snap NotificationsSnapshot
	if err := json.Unmarshal([]byte(first.data), &snap); err != nil || snap.Unread != 0 {
		t.Fatalf("initial snap = %s (%+v) err=%v", first.data, snap, err)
	}

	if code, m := do(t, h, "POST", "/api/notifications", `{"action":"send","severity":"info","event":"core.update","path":"/settings/updates","title":"Core updated"}`, nil); code != 200 {
		t.Fatalf("send: %d %v", code, m)
	}
	ev1, ok1 := readSSE(rd, 2*time.Second)
	ev2, ok2 := readSSE(rd, 2*time.Second)
	if !ok1 || !ok2 || ev1.event != "entry" || ev2.event != "snapshot" {
		t.Fatalf("stream after send: %+v (%v) / %+v (%v)", ev1, ok1, ev2, ok2)
	}
	var entry NotificationLogEntry
	if err := json.Unmarshal([]byte(ev1.data), &entry); err != nil || entry.Event != "core.update" || entry.Path != "/settings/updates" {
		t.Fatalf("streamed entry = %+v err=%v", entry, err)
	}
	if err := json.Unmarshal([]byte(ev2.data), &snap); err != nil || snap.Unread != 1 || snap.LatestID != entry.Id {
		t.Fatalf("streamed snap = %+v err=%v", snap, err)
	}

	var mr MarkReadResponse
	if code, m := do(t, h, "POST", "/api/notifications/read", `{}`, &mr); code != 200 || mr.ReadUpTo != entry.Id || mr.Unread != 0 {
		t.Fatalf("mark read: %d %+v (%v)", code, mr, m)
	}
	ev3, ok3 := readSSE(rd, 2*time.Second)
	if !ok3 || ev3.event != "snapshot" {
		t.Fatalf("stream after mark read: %+v (%v)", ev3, ok3)
	}
	if err := json.Unmarshal([]byte(ev3.data), &snap); err != nil || snap.Unread != 0 || snap.ReadUpTo != entry.Id {
		t.Fatalf("snap after mark read = %+v err=%v", snap, err)
	}
}
