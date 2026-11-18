package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// A logger created before the destination is chosen must follow the
// handler installed later (the CLI's stderr handler), not pin the first one.
func TestLoggerFollowsLaterDefault(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)

	l := Logger("cmdrunner")
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	l.Info("hidden")
	l.Warn("shown", "k", "v")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatalf("info leaked past WARN level: %s", out)
	}
	if !strings.Contains(out, `"module":"cmdrunner"`) || !strings.Contains(out, `"k":"v"`) {
		t.Fatalf("missing attrs: %s", out)
	}
}

// countingHandler counts how often a lazy logger derives a handler from it.
type countingHandler struct {
	slog.Handler
	derived *int
}

func (h countingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	*h.derived++
	return h.Handler.WithAttrs(as)
}

func TestLazyLoggerResolvesOncePerDefault(t *testing.T) {
	prev := slog.Default()
	defer slog.SetDefault(prev)

	l := Logger("cmdrunner")
	var buf bytes.Buffer
	first := 0
	slog.SetDefault(slog.New(&countingHandler{Handler: slog.NewJSONHandler(&buf, nil), derived: &first}))
	l.Info("one")
	l.Info("two")
	if first != 1 {
		t.Fatalf("two records resolved the handler %d times, want 1", first)
	}

	second := 0
	slog.SetDefault(slog.New(&countingHandler{Handler: slog.NewJSONHandler(&buf, nil), derived: &second}))
	l.Info("three")
	if second != 1 {
		t.Fatalf("after SetDefault the next record resolved %d times, want 1", second)
	}
	if n := strings.Count(buf.String(), `"module":"cmdrunner"`); n != 3 {
		t.Fatalf("want 3 tagged records, got %d:\n%s", n, buf.String())
	}
}

func TestSyslogPrefixFollowsLevel(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewJSONHandler(syslogPrefix{&buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l.Error("e")
	l.Warn("w")
	l.Info("i")
	l.Debug("d")
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		got = append(got, line[:3])
	}
	if want := []string{"<3>", "<4>", "<6>", "<7>"}; strings.Join(got, "") != strings.Join(want, "") {
		t.Fatalf("prefixes %v, want %v", got, want)
	}
}
