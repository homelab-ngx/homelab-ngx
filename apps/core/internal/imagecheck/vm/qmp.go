package vm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// QMPEvent is one event QEMU sent: RESET when the guest restarts, SHUTDOWN
// when it turns off or is told to, POWERDOWN for an ACPI power button.
type QMPEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
	At    time.Time       `json:"-"`
}

// qmp is a connection to QEMU's monitor. Commands are answered in order;
// events go to the callback as they come.
type qmp struct {
	conn    net.Conn
	mu      sync.Mutex
	replies chan qmpReply
}

type qmpReply struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
}

// dialQMP connects to the socket, which QEMU makes when it starts, and
// turns on commands.
func dialQMP(path string, wait time.Duration, onEvent func(QMPEvent)) (*qmp, error) {
	var conn net.Conn
	var err error
	end := time.Now().Add(wait)
	for {
		conn, err = net.Dial("unix", path)
		if err == nil || time.Now().After(end) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("QMP: %w", err)
	}
	q := &qmp{conn: conn, replies: make(chan qmpReply, 1)}
	r := bufio.NewReader(conn)
	// The greeting.
	if _, err := r.ReadBytes('\n'); err != nil {
		conn.Close()
		return nil, fmt.Errorf("QMP greeting: %w", err)
	}
	go q.read(r, onEvent)
	if _, err := q.Execute("qmp_capabilities", nil); err != nil {
		conn.Close()
		return nil, err
	}
	return q, nil
}

func (q *qmp) read(r *bufio.Reader, onEvent func(QMPEvent)) {
	defer close(q.replies)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var probe struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &probe) != nil {
			continue
		}
		if probe.Event != "" {
			var ev QMPEvent
			if json.Unmarshal(line, &ev) == nil {
				ev.At = time.Now()
				onEvent(ev)
			}
			continue
		}
		var rep qmpReply
		if json.Unmarshal(line, &rep) == nil {
			q.replies <- rep
		}
	}
}

// Execute runs a command and returns its answer.
func (q *qmp) Execute(cmd string, args any) (json.RawMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	msg := map[string]any{"execute": cmd}
	if args != nil {
		msg["arguments"] = args
	}
	b, _ := json.Marshal(msg)
	if _, err := q.conn.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("QMP %s: %w", cmd, err)
	}
	select {
	case rep, ok := <-q.replies:
		if !ok {
			return nil, fmt.Errorf("QMP %s: the connection closed", cmd)
		}
		if rep.Error != nil {
			return nil, fmt.Errorf("QMP %s: %s: %s", cmd, rep.Error.Class, rep.Error.Desc)
		}
		return rep.Return, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("QMP %s: no answer in 30 s", cmd)
	}
}

func (q *qmp) Close() error { return q.conn.Close() }
