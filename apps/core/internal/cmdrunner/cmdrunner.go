package cmdrunner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	logging "homelab-ngx.com/core/internal/logging"
)

type Escalation int

const (
	EscalateNone Escalation = iota
	EscalateAsUser
)

func (e Escalation) String() string {
	switch e {
	case EscalateNone:
		return "none"
	case EscalateAsUser:
		return "impersonate"
	default:
		return "unknown"
	}
}

type OutputMode int

const (
	OutputRaw OutputMode = iota
	OutputVerbose
	OutputCombined
)

type Builder struct {
	callerModule   string
	name           string
	args           []string
	user           string
	escalation     Escalation
	cwd            string
	env            []string
	ctx            context.Context
	timeout        time.Duration
	stdin          io.Reader
	lineHandler    func(stream, line string)
	outputMode     OutputMode
	expectExit     []int
	callerCanceled bool // set by logAndError; logs the failure at DEBUG
}

type Result struct {
	Cmd       string
	Args      []string
	ExitCode  int
	Stdout    string
	Stderr    string
	Output    string
	Duration  time.Duration
	Cancelled bool
}

func (r Result) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("$ %s\n", strings.Join(r.Args, " ")))
	if r.Stdout != "" {
		sb.WriteString(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			sb.WriteString("\n")
		}
	}
	if r.Stderr != "" {
		sb.WriteString("[stderr]\n")
		sb.WriteString(r.Stderr)
		if !strings.HasSuffix(r.Stderr, "\n") {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

type Error struct {
	Result Result
	Err    error
}

func (e *Error) Error() string {
	if e.Result.ExitCode > 0 {
		return fmt.Sprintf("command %s exited with code %d: %s", e.Result.Cmd, e.Result.ExitCode, strings.TrimSpace(e.Result.Stderr))
	}
	return fmt.Sprintf("command %s failed: %v", e.Result.Cmd, e.Err)
}

func (e *Error) Unwrap() error {
	return e.Err
}

var canonicalModules = map[string]struct{}{
	"bug": {}, "api": {}, "monitor": {}, "middleware": {},
	"storage": {}, "updates": {}, "cmdrunner": {},
	"systemd": {}, "services": {}, "backups": {}, "notifications": {},
	"sysupdates": {}, "system": {}, "doctor": {}, "stacks": {}, "drafts": {}, "audit": {},
	"hostcfg": {}, "installer": {},
}

var cmdrunnerLog = logging.CmdLogger()

const slowCommand = time.Second

var forbiddenCommandNames = map[string]struct{}{
	"sudo":   {},
	"pkexec": {},
	"doas":   {},
	"su":     {},
	"sh":     {},
	"bash":   {},
}

// New creates a Builder for a single command. name is resolved to an
// absolute path via exec.LookPath. args are the command arguments.
// Callers that previously used "sh -c" should use .WithCwd(), .WithEnv(),
// and explicit args instead.
func New(callerModule, name string, args ...string) *Builder {
	if _, ok := canonicalModules[callerModule]; !ok {
		panic(fmt.Sprintf("cmdrunner: invalid caller module %q; must be canonical module", callerModule))
	}
	if name == "" {
		panic("cmdrunner: command name cannot be empty")
	}
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	if _, forbidden := forbiddenCommandNames[base]; forbidden {
		panic(fmt.Sprintf(
			"cmdrunner: refusing %q as command name. Use .AsUser(<user>) for escalation. "+
				"Passing the escalation wrapper inline makes the cmd log line report "+
				"escalation=\"none\" even when it isn't.",
			name,
		))
	}
	return &Builder{
		callerModule: callerModule,
		name:         name,
		args:         args,
		escalation:   EscalateNone,
		outputMode:   OutputRaw,
	}
}

var forbiddenUIDs = map[int]struct{}{0: {}} // never impersonate root

func (b *Builder) AsUser(userName string) *Builder {
	u, err := user.Lookup(userName)
	if err != nil {
		panic(fmt.Sprintf("cmdrunner: AsUser(%q): user not found", userName))
	}
	uid, _ := strconv.Atoi(u.Uid)
	if _, forbidden := forbiddenUIDs[uid]; forbidden {
		panic(fmt.Sprintf("cmdrunner: AsUser(%q): forbidden — cannot impersonate root", userName))
	}
	b.user = userName
	b.escalation = EscalateAsUser
	return b
}

// nixosUserPath builds a PATH that mirrors the standard NixOS login
// environment: wrappers, user-local, nix profiles (home-manager, per-user
// NixOS, default), system path, and finally standard Unix fallbacks.
func nixosUserPath(homeDir, userName string) string {
	return fmt.Sprintf(
		"/run/wrappers/bin:%s/.local/bin:%s/.nix-profile/bin:/nix/profile/bin:"+
			"%s/.local/state/nix/profiles/profile/bin:/etc/profiles/per-user/%s/bin:"+
			"/nix/var/nix/profiles/default/bin:/run/current-system/sw/bin:/usr/bin:/bin",
		homeDir, homeDir, homeDir, userName,
	)
}

// resolveCommand finds name in the given PATH string. Uses exec.LookPath
// internally, temporarily swapping PATH so user-profile binaries are
// discoverable when AsUser is set.
var resolveCmdMu sync.Mutex

func resolveCommand(name, path string) (string, error) {
	resolveCmdMu.Lock()
	defer resolveCmdMu.Unlock()
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", path)
	defer os.Setenv("PATH", oldPath)
	return exec.LookPath(name)
}

func (b *Builder) WithCwd(dir string) *Builder {
	b.cwd = dir
	return b
}

func (b *Builder) WithEnv(kvs ...string) *Builder {
	b.env = append(b.env, kvs...)
	return b
}

func (b *Builder) WithContext(ctx context.Context) *Builder {
	b.ctx = ctx
	return b
}

func (b *Builder) WithTimeout(d time.Duration) *Builder {
	b.timeout = d
	return b
}

func (b *Builder) WithStdin(r io.Reader) *Builder {
	b.stdin = r
	return b
}

func (b *Builder) WithLineHandler(fn func(stream, line string)) *Builder {
	b.lineHandler = fn
	return b
}

// ExpectExit marks non-zero exit codes that are a normal answer rather than
// a failure (`systemctl is-active` → 3 for an inactive unit). The command
// still returns its *Error; only the log line drops to DEBUG.
func (b *Builder) ExpectExit(codes ...int) *Builder {
	b.expectExit = append(b.expectExit, codes...)
	return b
}

func (b *Builder) Output(mode OutputMode) *Builder {
	b.outputMode = mode
	return b
}

func (b *Builder) buildCredential() (*syscall.Credential, error) {
	u, err := user.Lookup(b.user)
	if err != nil {
		return nil, fmt.Errorf("lookup user %q: %w", b.user, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	gidStrs, _ := u.GroupIds()
	gids := make([]uint32, len(gidStrs))
	for i, g := range gidStrs {
		v, _ := strconv.Atoi(g)
		gids[i] = uint32(v)
	}
	return &syscall.Credential{
		Uid:    uint32(uid),
		Gid:    uint32(gid),
		Groups: gids,
	}, nil
}

func (b *Builder) Run() (Result, error) {
	start := time.Now()

	// Resolve absolute path. When impersonating, search the user's NixOS
	// profile paths so home-manager / nix-profile binaries are found.
	var absCmd string
	var err error
	if b.escalation == EscalateAsUser && b.user != "" {
		u, _ := user.Lookup(b.user)
		absCmd, err = resolveCommand(b.name, nixosUserPath(u.HomeDir, u.Username))
	} else {
		absCmd, err = exec.LookPath(b.name)
	}
	if err != nil {
		return b.logAndError(Result{ExitCode: -1}, start,
			fmt.Errorf("command %q not found in PATH: %w", b.name, err), b.ctx)
	}

	var cred *syscall.Credential
	if b.escalation == EscalateAsUser && b.user != "" {
		cred, err = b.buildCredential()
		if err != nil {
			return b.logAndError(Result{ExitCode: -1}, start,
				fmt.Errorf("credential for user %q: %w", b.user, err), b.ctx)
		}
	}

	res := Result{
		Cmd:      absCmd,
		Args:     append([]string{absCmd}, b.args...),
		ExitCode: -1,
	}

	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if b.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, b.timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, absCmd, b.args...)
	if b.cwd != "" {
		cmd.Dir = b.cwd
	}
	if cred != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: cred}
	}
	if b.stdin != nil {
		cmd.Stdin = b.stdin
	}

	// Build clean environment for impersonated processes.
	if b.escalation == EscalateAsUser && b.user != "" {
		u, _ := user.Lookup(b.user)
		env := []string{
			"PATH=" + nixosUserPath(u.HomeDir, u.Username),
			"HOME=" + u.HomeDir,
			"USER=" + u.Username,
			"LOGNAME=" + u.Username,
		}
		env = append(env, b.env...)
		cmd.Env = env
	} else if len(b.env) > 0 {
		cmd.Env = append(os.Environ(), b.env...)
	}

	// Pipes
	var stdoutPipe, stderrPipe io.ReadCloser
	var stdoutBuf, stderrBuf strings.Builder

	if b.lineHandler != nil || b.outputMode == OutputCombined {
		stdoutPipe, err = cmd.StdoutPipe()
		if err != nil {
			return b.logAndError(res, start, fmt.Errorf("stdout pipe: %w", err), ctx)
		}
		stderrPipe, err = cmd.StderrPipe()
		if err != nil {
			_ = stdoutPipe.Close()
			return b.logAndError(res, start, fmt.Errorf("stderr pipe: %w", err), ctx)
		}
	} else {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	}

	if err := cmd.Start(); err != nil {
		return b.logAndError(res, start, fmt.Errorf("start process: %w", err), ctx)
	}

	if stdoutPipe != nil && stderrPipe != nil {
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stdoutPipe)
			for scanner.Scan() {
				line := scanner.Text()
				if b.lineHandler != nil {
					b.lineHandler("stdout", line)
				}
				stdoutBuf.WriteString(line + "\n")
			}
		}()

		go func() {
			defer wg.Done()
			scanner := bufio.NewScanner(stderrPipe)
			for scanner.Scan() {
				line := scanner.Text()
				if b.lineHandler != nil {
					b.lineHandler("stderr", line)
				}
				if b.outputMode == OutputCombined {
					stdoutBuf.WriteString(line + "\n")
				} else {
					stderrBuf.WriteString(line + "\n")
				}
			}
		}()

		wg.Wait()
	}

	err = cmd.Wait()
	res.Duration = time.Since(start)
	res.Stdout = stdoutBuf.String()
	res.Stderr = stderrBuf.String()

	if ctx.Err() != nil {
		res.Cancelled = true
	}

	switch b.outputMode {
	case OutputVerbose:
		res.Output = res.String()
	case OutputCombined:
		res.Output = res.Stdout
	default:
		res.Output = res.Stdout
	}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				res.ExitCode = status.ExitStatus()
			} else {
				res.ExitCode = exitErr.ExitCode()
			}
		}
		return b.logAndError(res, start, err, ctx)
	}

	res.ExitCode = 0
	b.logResult(res, start, nil)
	return res, nil
}

func (b *Builder) logAndError(res Result, start time.Time, err error, ctx context.Context) (Result, error) {
	res.Duration = time.Since(start)
	if ctx != nil && ctx.Err() != nil {
		res.Cancelled = true
		// The caller gave up (an API client hung up), as opposed to a
		// deadline expiring, which still deserves a WARN/ERROR.
		b.callerCanceled = errors.Is(ctx.Err(), context.Canceled)
	}
	b.logResult(res, start, err)
	return res, &Error{Result: res, Err: err}
}

func (b *Builder) summary() string {
	base := b.name
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	full := base
	if len(b.args) > 0 {
		full = base + " " + strings.Join(b.args, " ")
	}
	const maxLen = 100
	if len(full) > maxLen {
		full = full[:maxLen-1] + "…"
	}
	return full
}

func (b *Builder) logResult(res Result, start time.Time, err error) {
	durMs := int(res.Duration.Milliseconds())
	logger := logging.CmdLogger()
	cmdSummary := b.summary()

	args := res.Args

	if err != nil {
		if b.callerCanceled {
			logger.Debug(
				fmt.Sprintf("%s → canceled by caller (%dms)", cmdSummary, durMs),
				"cmd", res.Cmd,
				"args", args,
				"duration_ms", durMs,
				"caller_module", b.callerModule,
				"error", err.Error(),
			)
		} else if res.ExitCode > 0 && slices.Contains(b.expectExit, res.ExitCode) {
			logger.Debug(
				fmt.Sprintf("%s → exit %d, expected (%dms)", cmdSummary, res.ExitCode, durMs),
				"cmd", res.Cmd,
				"args", args,
				"escalation", b.escalation.String(),
				"exit_code", res.ExitCode,
				"duration_ms", durMs,
				"caller_module", b.callerModule,
			)
		} else if res.ExitCode > 0 {
			logger.Warn(
				fmt.Sprintf("%s → exit %d (%dms)", cmdSummary, res.ExitCode, durMs),
				"cmd", res.Cmd,
				"args", args,
				"escalation", b.escalation.String(),
				"exit_code", res.ExitCode,
				"duration_ms", durMs,
				"cancelled", res.Cancelled,
				"caller_module", b.callerModule,
				"error", err.Error(),
			)
		} else {
			logger.Error(
				fmt.Sprintf("%s → exec failed: %v", cmdSummary, err),
				"cmd", res.Cmd,
				"args", args,
				"escalation", b.escalation.String(),
				"exit_code", res.ExitCode,
				"duration_ms", durMs,
				"cancelled", res.Cancelled,
				"caller_module", b.callerModule,
				"error", err.Error(),
			)
		}
	} else {
		level := slog.LevelDebug
		if res.Duration > slowCommand {
			level = slog.LevelInfo
		}
		logger.Log(context.Background(), level,
			fmt.Sprintf("%s → ok (%dms)", cmdSummary, durMs),
			"cmd", res.Cmd,
			"args", args,
			"escalation", b.escalation.String(),
			"exit_code", res.ExitCode,
			"duration_ms", durMs,
			"cancelled", res.Cancelled,
			"caller_module", b.callerModule,
		)
	}
}
