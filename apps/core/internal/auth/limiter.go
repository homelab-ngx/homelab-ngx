package auth

import (
	"sync"
	"time"
)

// The sign-in limits: a client gets clientFailures wrong passwords per
// window, and every client together gets globalFailures. A right password
// clears the client's count.
const (
	limitWindow    = 15 * time.Minute
	clientFailures = 5
	globalFailures = 100
)

// limiter counts failed sign-ins in memory. A daemon restart clears it.
type limiter struct {
	mu      sync.Mutex
	clients map[string][]time.Time
	global  []time.Time
}

func newLimiter() *limiter { return &limiter{clients: map[string][]time.Time{}} }

// wait is how long client must wait before its next attempt; zero when it
// may try now.
func (l *limiter) wait(client string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global = recent(l.global, now)
	c := recent(l.clients[client], now)
	if len(c) == 0 {
		delete(l.clients, client)
	} else {
		l.clients[client] = c
	}
	var d time.Duration
	if len(c) >= clientFailures {
		d = c[len(c)-clientFailures].Add(limitWindow).Sub(now)
	}
	if len(l.global) >= globalFailures {
		d = max(d, l.global[len(l.global)-globalFailures].Add(limitWindow).Sub(now))
	}
	return d
}

func (l *limiter) fail(client string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.clients[client] = append(recent(l.clients[client], now), now)
	l.global = append(recent(l.global, now), now)
}

func (l *limiter) succeed(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, client)
}

// recent drops the times older than the window.
func recent(ts []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= limitWindow {
		i++
	}
	return ts[i:]
}
