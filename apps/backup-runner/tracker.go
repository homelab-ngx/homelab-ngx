package main

import (
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"
)

// progressEvery is the least time between two state writes that only carry
// progress. Item transitions are always written at once.
const progressEvery = time.Second

// tracker owns the run's state. Events arrive on one channel and one
// goroutine applies them and rewrites the state file, so no lock guards the
// state and the file is whole after every event.
type tracker struct {
	path   string
	events chan event
	done   chan struct{}
	log    *lineLog
}

type event struct {
	apply func(*runfile.State)
	// progress events may be coalesced; the others are written immediately.
	progress bool
	// applied is closed once the event is in the state file (do only).
	applied chan struct{}
}

func newTracker(path string, st *runfile.State, log *lineLog) *tracker {
	t := &tracker{path: path, events: make(chan event, 64), done: make(chan struct{}), log: log}
	t.write(st)
	go t.loop(st)
	return t
}

func (t *tracker) loop(st *runfile.State) {
	defer close(t.done)
	last := time.Now()
	// flush fires when a held-back progress write is due; nil while none is.
	var flush <-chan time.Time
	for {
		select {
		case ev, ok := <-t.events:
			if !ok {
				if flush != nil {
					t.write(st)
				}
				return
			}
			t.apply(ev, st)
			st.UpdatedAt = time.Now()
			if wait := progressEvery - time.Since(last); ev.progress && wait > 0 {
				if flush == nil {
					flush = time.After(wait)
				}
			} else {
				t.write(st)
				last, flush = time.Now(), nil
			}
			if ev.applied != nil {
				close(ev.applied)
			}
		case <-flush:
			t.write(st)
			last, flush = time.Now(), nil
		}
	}
}

// apply runs one event. An event that panics is dropped: the state keeps
// what it had, and the run goes on.
func (t *tracker) apply(ev event, st *runfile.State) {
	defer func() {
		if p := recover(); p != nil {
			t.log.printf("state event panicked: %v", p)
		}
	}()
	ev.apply(st)
}

func (t *tracker) write(st *runfile.State) {
	if err := runfile.WriteState(t.path, st); err != nil {
		t.log.printf("write state: %v", err)
	}
}

// do applies an item transition and returns once it is on disk.
func (t *tracker) do(fn func(*runfile.State)) {
	ev := event{apply: fn, applied: make(chan struct{})}
	t.events <- ev
	<-ev.applied
}

// progress applies a progress update; its write may wait for the next event.
func (t *tracker) progress(fn func(*runfile.State)) {
	t.events <- event{apply: fn, progress: true}
}

// close stops the tracker after the last write.
func (t *tracker) close() {
	close(t.events)
	<-t.done
}
