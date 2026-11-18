package system

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
	"homelab-ngx.com/core/internal/config"
)

type fakeStacks []StackUnit

func (f fakeStacks) LogUnits(context.Context) []StackUnit { return f }

func (fakeStacks) BackupLogUnits(context.Context) []BackupUnit { return nil }

// fakeSources is the stacks' units plus the backups' units.
type fakeSources struct {
	fakeStacks
	backups []BackupUnit
}

func (f fakeSources) BackupLogUnits(context.Context) []BackupUnit { return f.backups }

func journalLine(cursor string, us int64, prio int, msg string) string {
	return fmt.Sprintf(`{"__CURSOR":%q,"__REALTIME_TIMESTAMP":"%d","PRIORITY":"%d","MESSAGE":%q}`, cursor, us, prio, msg)
}

func logsFixture(t *testing.T) (*System, *cmdrunnertest.Fake, string) {
	t.Helper()
	dir := t.TempDir()
	snap := &config.Snapshot{Nix: &config.Nix{CoreServices: []string{"homelab-daemon.service"}}}
	f := cmdrunnertest.NewFake()
	stacks := fakeStacks{
		{Name: "media-sonarr", Unit: "sonarr.service", JournalDir: dir},
		{Name: "mail-bridge", Unit: "mail-bridge.service", JournalDir: filepath.Join(dir, "missing")},
	}
	return New(&fakeCfg{snap: snap}, f, stacks), f, dir
}

// logsQuery renders p as the query of GET /logs, leaving out zero values
// the way a client leaves out absent parameters.
func logsQuery(p GetLogsParams) string {
	q := url.Values{"unit": {p.Unit}}
	for k, v := range map[string]string{"before": p.Before, "after": p.After, "q": p.Q} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if p.Lines != 0 {
		q.Set("lines", strconv.Itoa(p.Lines))
	}
	if p.Priority != 0 {
		q.Set("priority", strconv.Itoa(p.Priority))
	}
	return "/api/logs?" + q.Encode()
}

func getLogs(t *testing.T, s *System, p GetLogsParams) LogPage {
	t.Helper()
	var page LogPage
	if code, body := serve(t, s, logsQuery(p), &page); code != 200 {
		t.Fatalf("GET /logs: %d %s", code, body)
	}
	return page
}

func cursors(p LogPage) []string {
	var out []string
	for _, e := range p.Entries {
		out = append(out, e.Cursor)
	}
	return out
}

func TestGetLogsHeadReadsHostJournalNewestFirst(t *testing.T) {
	t.Parallel()
	s, f, _ := logsFixture(t)
	f.On("journalctl").Reply(strings.Join([]string{
		journalLine("c3", 3_000_000, 4, `{"level":"WARN"}`),
		journalLine("c2", 2_000_000, 6, "two"),
		journalLine("c1", 1_000_000, 6, "one"),
	}, "\n"))
	page := getLogs(t, s, GetLogsParams{Unit: "homelab-daemon", Lines: 2, Priority: 4, Q: "a.b(c"})

	if got := cursors(page); !slices.Equal(got, []string{"c3", "c2"}) || !page.More {
		t.Fatalf("cursors %v more %v", got, page.More)
	}
	if e := page.Entries[0]; e.Priority != 4 || e.Message != `{"level":"WARN"}` || e.Time.Unix() != 3 {
		t.Fatalf("entry %+v", e)
	}
	args := f.Calls()[0].Args
	want := []string{"--no-pager", "--quiet", "--output=json", "--unit=homelab-daemon.service",
		"--priority=4", "--case-sensitive=false", `--grep=a\.b\(c`, "--reverse", "--lines=3"}
	if !slices.Equal(args, want) {
		t.Fatalf("args %v\nwant %v", args, want)
	}
}

func TestGetLogsOlderDropsTheCursorLine(t *testing.T) {
	t.Parallel()
	s, f, dir := logsFixture(t)
	f.On("journalctl").Reply(journalLine("c2", 2, 6, "b") + "\n" + journalLine("c1", 1, 6, "a") + "\n")
	page := getLogs(t, s, GetLogsParams{Unit: "media-sonarr", Lines: 5, Before: "c2"})

	if got := cursors(page); !slices.Equal(got, []string{"c1"}) || page.More {
		t.Fatalf("cursors %v more %v", got, page.More)
	}
	args := f.Calls()[0].Args
	for _, a := range []string{"--unit=sonarr.service", "--directory=" + dir, "--reverse", "--cursor=c2", "--lines=7"} {
		if !slices.Contains(args, a) {
			t.Fatalf("args %v lack %s", args, a)
		}
	}
}

func TestGetLogsNewerComesBackNewestFirst(t *testing.T) {
	t.Parallel()
	s, f, _ := logsFixture(t)
	f.On("journalctl").Reply(journalLine("c4", 4, 6, "d") + "\n" + journalLine("c5", 5, 6, "e") + "\n" + journalLine("c6", 6, 6, "f"))
	page := getLogs(t, s, GetLogsParams{Unit: "homelab-daemon", Lines: 2, After: "c3"})

	// More new lines than fit: the client starts over from the head.
	if got := cursors(page); !slices.Equal(got, []string{"c5", "c4"}) || !page.More {
		t.Fatalf("cursors %v more %v", got, page.More)
	}
	if args := f.Calls()[0].Args; !slices.Contains(args, "--after-cursor=c3") || slices.Contains(args, "--reverse") {
		t.Fatalf("args %v", args)
	}
}

func TestGetLogsNoMatchIsEmpty(t *testing.T) {
	t.Parallel()
	s, f, _ := logsFixture(t)
	f.On("journalctl").Fail("")
	if page := getLogs(t, s, GetLogsParams{Unit: "homelab-daemon", Q: "nothing"}); len(page.Entries) != 0 {
		t.Fatalf("entries %v", page.Entries)
	}
}

func TestGetLogsStackWithoutJournalIsEmpty(t *testing.T) {
	t.Parallel()
	s, f, _ := logsFixture(t)
	if page := getLogs(t, s, GetLogsParams{Unit: "mail-bridge"}); len(page.Entries) != 0 {
		t.Fatalf("entries %v", page.Entries)
	}
	if len(f.Calls()) != 0 {
		t.Fatal("journalctl ran for a missing journal")
	}
}

func TestGetLogsErrors(t *testing.T) {
	t.Parallel()
	s, f, _ := logsFixture(t)
	f.On("journalctl").Fail("Failed to seek to cursor: Invalid argument")
	cases := []struct {
		p    GetLogsParams
		want int
	}{
		{GetLogsParams{Unit: "nope"}, 404},
		{GetLogsParams{Unit: "homelab-daemon", Lines: 501}, 422},
		{GetLogsParams{Unit: "homelab-daemon", Priority: 8}, 422},
		{GetLogsParams{Unit: "homelab-daemon", Before: "a", After: "b"}, 400},
		{GetLogsParams{Unit: "homelab-daemon", Before: "garbage"}, 400},
	}
	for _, c := range cases {
		if got, body := serve(t, s, logsQuery(c.p), nil); got != c.want {
			t.Errorf("%+v: status %d, want %d (%s)", c.p, got, c.want, body)
		}
	}
}

func TestJournalMessageBytes(t *testing.T) {
	t.Parallel()
	if got := journalMessage([]byte(`[104,105,255]`)); got != "hi�" {
		t.Fatalf("got %q", got)
	}
}
