package cmdrunner

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestEchoSuccess(t *testing.T) {
	t.Parallel()
	res, err := New("api", "echo", "hello").Run()
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("expected exit code 0, got: %d", res.ExitCode)
	}
	trimmed := strings.TrimSpace(res.Stdout)
	if trimmed != "hello" {
		t.Errorf("expected stdout 'hello', got: %q", res.Stdout)
	}
	if res.Output != res.Stdout {
		t.Errorf("expected Output to match Stdout under OutputRaw, got %q", res.Output)
	}
}

func TestCommandFailed(t *testing.T) {
	t.Parallel()
	_, err := New("api", "false").Run()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var cmdErr *Error
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected error of type *cmdrunner.Error, got: %T", err)
	}

	if cmdErr.Result.ExitCode != 1 {
		t.Errorf("expected exit code 1, got: %d", cmdErr.Result.ExitCode)
	}
}

func TestWithLineHandler(t *testing.T) {
	t.Parallel()
	var lines []string
	res, err := New("api", "echo", "hello\nworld").
		WithLineHandler(func(stream, line string) {
			lines = append(lines, line)
		}).
		Run()

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got: %v", lines)
	}

	if lines[0] != "hello" || lines[1] != "world" {
		t.Errorf("expected hello/world, got: %v", lines)
	}

	trimmedStdout := strings.TrimSpace(res.Stdout)
	if trimmedStdout != "hello\nworld" {
		t.Errorf("expected res.Stdout 'hello\\nworld', got %q", res.Stdout)
	}
}

func TestTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := New("api", "sleep", "1").
		WithContext(ctx).
		Run()

	if err == nil {
		t.Fatal("expected error from timeout, got nil")
	}

	var cmdErr *Error
	if !errors.As(err, &cmdErr) {
		t.Fatalf("expected error to wrap *cmdrunner.Error, got %T", err)
	}

	if !cmdErr.Result.Cancelled {
		t.Errorf("expected Result.Cancelled to be true, got false")
	}
}

// TestRejectsInlineEscalation ensures New() panics when a caller tries to
// pass sudo/pkexec/doas/su/sh/bash as the command name instead of using the
// builder methods.
func TestRejectsInlineEscalation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  string
	}{
		{"sudo bare", "sudo"},
		{"sudo absolute", "/run/wrappers/bin/sudo"},
		{"sudo /usr/bin", "/usr/bin/sudo"},
		{"pkexec bare", "pkexec"},
		{"pkexec absolute", "/run/wrappers/bin/pkexec"},
		{"doas", "doas"},
		{"su", "/usr/bin/su"},
		{"sh", "sh"},
		{"bash", "bash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic for %q, got none", tc.cmd)
				}
				msg, _ := r.(string)
				if !strings.Contains(msg, "AsUser") {
					t.Errorf("panic message should mention .AsUser(), got: %q", msg)
				}
			}()
			_ = New("api", tc.cmd, "-i", "-u", "alice", "echo", "hi")
		})
	}
}

// TestAllowsLegitimateCommands sanity-checks the forbidden list doesn't
// reject normal commands that happen to share substrings.
func TestAllowsLegitimateCommands(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"git", "echo", "systemctl", "bcachefs", "sudoers-thing-not-real"} {
		// Should not panic.
		_ = New("api", cmd)
	}
}

func TestBuildAccumulatesFields(t *testing.T) {
	t.Parallel()
	b := New("api", "echo", "hello").
		AsUser("nobody").
		WithCwd("/tmp").
		WithEnv("FOO=bar", "BAZ=qux")

	if b.escalation != EscalateAsUser || b.user != "nobody" {
		t.Errorf("expected EscalateAsUser and user 'nobody', got: %v %q", b.escalation, b.user)
	}
	if b.cwd != "/tmp" {
		t.Errorf("expected cwd '/tmp', got %q", b.cwd)
	}
}

func TestCommandNotFound(t *testing.T) {
	t.Parallel()
	_, err := New("api", "nonexistent-command-zzz123").Run()
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
	if !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("error should mention PATH, got: %v", err)
	}
}

// captureLogs routes slog.Default to a JSON buffer at DEBUG for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestExpectExitLogsAtDebug(t *testing.T) {
	cases := []struct {
		name   string
		expect []int
		level  string
	}{
		{"unexpected exit warns", nil, "WARN"},
		{"expected exit is debug", []int{1}, "DEBUG"},
		{"other expected code still warns", []int{3}, "WARN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			_, err := OS{}.Run(context.Background(), Cmd{Module: "api", Name: "false", ExpectExit: tc.expect})
			var cmdErr *Error
			if !errors.As(err, &cmdErr) || cmdErr.Result.ExitCode != 1 {
				t.Fatalf("want *Error with exit 1 regardless of ExpectExit, got %v", err)
			}
			if want := `"level":"` + tc.level + `"`; !strings.Contains(buf.String(), want) {
				t.Errorf("log line missing %s:\n%s", want, buf.String())
			}
		})
	}
}

func TestCallerCancelLogsAtDebugButDeadlineDoesNot(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		buf := captureLogs(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := (OS{}).Run(ctx, Cmd{Module: "api", Name: "sleep", Args: []string{"5"}}); err == nil {
			t.Fatal("want an error from a canceled command")
		}
		if out := buf.String(); strings.Contains(out, `"level":"ERROR"`) || strings.Contains(out, `"level":"WARN"`) {
			t.Errorf("caller cancel logged above DEBUG:\n%s", out)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		buf := captureLogs(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if _, err := (OS{}).Run(ctx, Cmd{Module: "api", Name: "sleep", Args: []string{"5"}}); err == nil {
			t.Fatal("want an error from a timed-out command")
		}
		if out := buf.String(); !strings.Contains(out, `"level":"ERROR"`) && !strings.Contains(out, `"level":"WARN"`) {
			t.Errorf("a timeout must still log as a failure:\n%s", out)
		}
	})
}

func TestSuccessLogLevelDependsOnDuration(t *testing.T) {
	cases := []struct {
		name  string
		took  time.Duration
		level string
	}{
		{"fast success is debug", 0, "DEBUG"},
		{"slow success stays info", slowCommand + 100*time.Millisecond, "INFO"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			// The level depends only on the measured duration; no process
			// needs to run that long.
			New("api", "sleep").logResult(Result{Cmd: "sleep", Duration: tc.took}, time.Now(), nil)
			if want := `"level":"` + tc.level + `"`; !strings.Contains(buf.String(), want) {
				t.Errorf("log line missing %s:\n%s", want, buf.String())
			}
		})
	}
}
