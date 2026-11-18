package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
)

const (
	defaultLogLines = 50
	maxLogLines     = 500
)

// Logs returns journal lines of one unit from the log viewer list, newest
// first. Host units read the host journal; a stack unit reads the stack's own
// journal directory, which exists whether or not the stack runs.
func (s *System) Logs(ctx context.Context, p GetLogsParams) (LogPage, error) {
	snap, err := s.snapshot()
	if err != nil {
		return LogPage{}, err
	}
	units := s.logUnits(ctx, snap)
	idx := slices.IndexFunc(units, func(u logUnit) bool { return u.ID == p.Unit })
	if idx < 0 {
		return LogPage{}, httpx.NotFound("unknown_unit", "no unit %q in the log viewer list", p.Unit)
	}
	if p.Before != "" && p.After != "" {
		return LogPage{}, httpx.BadRequest("invalid_query", "before and after cannot be combined")
	}
	u := units[idx]
	if u.JournalDir != "" {
		if _, err := os.Stat(u.JournalDir); errors.Is(err, os.ErrNotExist) {
			return LogPage{Entries: []LogEntry{}}, nil
		}
	}
	n := p.Lines
	if n <= 0 {
		n = defaultLogLines
	}
	n = min(n, maxLogLines)

	res, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "system", Name: "journalctl", Args: journalArgs(u, p, n),
		Output: cmdrunner.OutputRaw, ExpectExit: []int{1},
	})
	// journalctl exits 1 with no output when --grep matches nothing.
	if err != nil && !(res.ExitCode == 1 && strings.TrimSpace(res.Stdout+res.Stderr) == "") {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(msg, "cursor") {
			return LogPage{}, httpx.BadRequest("invalid_cursor", "%s", msg)
		}
		return LogPage{}, httpx.Internal("journal_failed", "reading the journal of %s: %s", u.ID, msg)
	}
	entries, err := parseJournal(res.Stdout)
	if err != nil {
		return LogPage{}, httpx.Internal("journal_failed", "parsing the journal of %s: %s", u.ID, err)
	}
	page := LogPage{Entries: []LogEntry{}}
	switch {
	case p.After != "":
		// Oldest first, starting right after the cursor.
		page.More = len(entries) > n
		entries = entries[:min(n, len(entries))]
		slices.Reverse(entries)
	case p.Before != "":
		// Newest first, starting at the cursor's own line.
		if len(entries) > 0 && entries[0].Cursor == p.Before {
			entries = entries[1:]
		}
		fallthrough
	default:
		page.More = len(entries) > n
		entries = entries[:min(n, len(entries))]
	}
	page.Entries = append(page.Entries, entries...)
	return page, nil
}

// journalArgs builds the journalctl call for one page. It asks for one line
// more than the page holds, so the caller knows whether more exist.
func journalArgs(u logUnit, p GetLogsParams, n int) []string {
	args := []string{"--no-pager", "--quiet", "--output=json", "--unit=" + u.Unit}
	if u.JournalDir != "" {
		args = append(args, "--directory="+u.JournalDir)
	}
	if p.Priority > 0 {
		args = append(args, "--priority="+strconv.Itoa(p.Priority))
	}
	if p.Q != "" {
		args = append(args, "--case-sensitive=false", "--grep="+regexp.QuoteMeta(p.Q))
	}
	switch {
	case p.After != "":
		args = append(args, "--after-cursor="+p.After, "--lines="+strconv.Itoa(n+1))
	case p.Before != "":
		args = append(args, "--reverse", "--cursor="+p.Before, "--lines="+strconv.Itoa(n+2))
	default:
		args = append(args, "--reverse", "--lines="+strconv.Itoa(n+1))
	}
	return args
}

// parseJournal reads journalctl's JSON output, one entry per line.
func parseJournal(out string) ([]LogEntry, error) {
	var entries []LogEntry
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e struct {
			Cursor   string          `json:"__CURSOR"`
			Realtime string          `json:"__REALTIME_TIMESTAMP"`
			Priority string          `json:"PRIORITY"`
			Message  json.RawMessage `json:"MESSAGE"`
		}
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, err
		}
		us, _ := strconv.ParseInt(e.Realtime, 10, 64)
		prio, err := strconv.Atoi(e.Priority)
		if err != nil {
			prio = 6
		}
		entries = append(entries, LogEntry{
			Cursor:   e.Cursor,
			Time:     time.UnixMicro(us).UTC(),
			Priority: prio,
			Message:  journalMessage(e.Message),
		})
	}
	return entries, sc.Err()
}

// journalMessage decodes MESSAGE, which journalctl gives as a string, or as
// an array of bytes when it is not valid UTF-8.
func journalMessage(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		b = make([]byte, len(ints))
		for i, v := range ints {
			b[i] = byte(v)
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	return ""
}
