package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"
)

// checkTimeout bounds a destination check, so an unreachable host fails the
// destination instead of holding the run.
const checkTimeout = 2 * time.Minute

// toolPath resolves a tool: the environment, then the build's pinned path,
// then PATH.
func toolPath(name, env, pinned string) (string, error) {
	if p := os.Getenv(env); p != "" {
		return p, nil
	}
	if pinned != "" {
		return pinned, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is not installed: %w", name, err)
	}
	return p, nil
}

// exitError turns a tool's exit into an error message: the last error line
// it printed, or the bare exit code.
func exitError(name string, code int, last string) string {
	if code < 0 {
		return fmt.Sprintf("%s was stopped by a signal", name)
	}
	if last != "" {
		return last
	}
	return fmt.Sprintf("%s exited with code %d", name, code)
}

// lastError remembers the newest error line a tool printed.
type lastError struct {
	mu  sync.Mutex
	msg string
}

func (l *lastError) set(s string) {
	s = strings.TrimSpace(s)
	if len(s) > 1000 {
		s = s[:1000] + "…"
	}
	l.mu.Lock()
	l.msg = s
	l.mu.Unlock()
}

func (l *lastError) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.msg
}

// ---- rclone ----

type rcloneTool struct {
	r    *runner
	dest runfile.Destination
	bin  string
	conf string
}

func (t *rcloneTool) prepare(ctx context.Context, _ bool) (string, error) {
	bin, err := toolPath("rclone", "RCLONE_BIN", rcloneBin)
	if err != nil {
		return "", err
	}
	t.bin = bin
	t.conf = filepath.Join(t.r.spec.WorkDir, fmt.Sprintf("dest-%d.rclone.conf", t.dest.ID))
	if err := os.WriteFile(t.conf, []byte(t.dest.Config), 0o600); err != nil {
		return "", fmt.Errorf("write rclone config: %w", err)
	}
	code, msg, err := t.lsd(ctx, t.dest.Remote)
	if err != nil {
		return "", err
	}
	// 3 is "directory not found": the remote answered and the folder is not
	// there yet, which the first run fixes. It does not fix a missing bucket.
	if code == 3 && t.dest.Bucket != "" {
		if t.dest.Bucket != t.dest.Remote {
			if code, msg, err = t.lsd(ctx, t.dest.Bucket); err != nil {
				return "", err
			}
		}
		if code == 3 {
			_, bucket, _ := strings.Cut(t.dest.Bucket, ":")
			return "", fmt.Errorf("the bucket %q does not exist, or this key cannot see it", bucket)
		}
	}
	if code == 0 || code == 3 {
		return "", nil
	}
	return "", errors.New(exitError("rclone", code, msg))
}

// lsd lists the top of remote: rclone's exit code and its last error line.
func (t *rcloneTool) lsd(ctx context.Context, remote string) (int, string, error) {
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	var last lastError
	code, err := runTool(cctx, t.bin, []string{
		"lsd", remote, "--max-depth", "1", "--config", t.conf,
		"--use-json-log", "--retries", "1", "--low-level-retries", "3",
	}, nil, func(line []byte) {
		var m rcloneLine
		if json.Unmarshal(line, &m) == nil && m.Msg != "" {
			if m.Level == "error" {
				last.set(m.Msg)
			}
			t.r.log.printf("rclone: %s: %s", m.Level, m.Msg)
			return
		}
		// lsd lists the folders it found on stdout; nobody needs them.
		if len(line) > 0 && line[0] == '{' {
			t.r.log.printf("rclone: %s", line)
		}
	})
	switch {
	case err != nil:
		return 0, "", err
	case cctx.Err() != nil && ctx.Err() == nil:
		return 0, "", fmt.Errorf("no answer from the destination within %s", checkTimeout)
	}
	return code, last.get(), nil
}

// rcloneLine is one line of rclone's JSON log. Stats lines carry stats.
type rcloneLine struct {
	Level string       `json:"level"`
	Msg   string       `json:"msg"`
	Stats *rcloneStats `json:"stats"`
}

type rcloneStats struct {
	Bytes          int64   `json:"bytes"`
	TotalBytes     int64   `json:"totalBytes"`
	Transfers      int64   `json:"transfers"`
	TotalTransfers int64   `json:"totalTransfers"`
	Speed          float64 `json:"speed"`
	Errors         int64   `json:"errors"`
}

func rcloneTarget(remote, sub string) string {
	sub = strings.Trim(sub, "/")
	if sub == "" {
		return remote
	}
	if strings.HasSuffix(remote, ":") || strings.HasSuffix(remote, "/") {
		return remote + sub
	}
	return remote + "/" + sub
}

func (t *rcloneTool) args(f runfile.Folder) []string {
	verb := "copy"
	if t.dest.DeleteRemoved {
		verb = "sync"
	}
	args := []string{
		verb, f.Path, rcloneTarget(t.dest.Remote, f.RemotePath),
		"--config", t.conf, "--one-file-system",
		"--use-json-log", "--stats", "2s", "--stats-log-level", "NOTICE",
	}
	if t.dest.DeleteRemoved && len(f.Files) == 0 {
		// What a skip pattern leaves out is not in the backup, so with delete
		// on it goes from the destination too, like a file removed on the host.
		// Not for a folder limited to a file list: there the destination folder
		// may hold other things.
		args = append(args, "--delete-excluded")
	}
	if f.SizeOnly {
		args = append(args, "--size-only")
	}
	if t.r.spec.Bwlimit != "" {
		args = append(args, "--bwlimit", t.r.spec.Bwlimit)
	}
	if t.r.spec.Transfers > 0 {
		args = append(args, "--transfers", strconv.Itoa(t.r.spec.Transfers))
	}
	for _, p := range f.Excludes {
		args = append(args, "--filter", "- "+p)
	}
	if len(f.Files) > 0 {
		for _, p := range f.Files {
			args = append(args, "--filter", "+ "+p)
		}
		args = append(args, "--filter", "- **")
	}
	return args
}

func (t *rcloneTool) copy(ctx context.Context, f runfile.Folder) outcome {
	var (
		last  lastError
		mu    sync.Mutex
		stats rcloneStats
	)
	dest := t.dest.Name
	code, err := runTool(ctx, t.bin, t.args(f), nil, func(line []byte) {
		var m rcloneLine
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &m) != nil || (m.Msg == "" && m.Stats == nil) {
			t.r.log.printf("rclone: %s", line)
			return
		}
		if m.Stats == nil {
			if m.Level == "error" {
				last.set(m.Msg)
			}
			t.r.log.printf("rclone: %s: %s", m.Level, m.Msg)
			return
		}
		s := *m.Stats
		mu.Lock()
		stats = s
		mu.Unlock()
		t.r.track.progress(func(st *runfile.State) {
			st.Progress = &runfile.Progress{
				Destination: dest, Folder: f.Label,
				BytesDone: s.Bytes, BytesTotal: s.TotalBytes,
				FilesDone: s.Transfers, FilesTotal: s.TotalTransfers, Speed: s.Speed,
			}
		})
	})
	mu.Lock()
	out := outcome{result: runfile.ResultSuccess, files: stats.Transfers, bytes: stats.Bytes}
	mu.Unlock()
	if err != nil {
		out.result, out.err = runfile.ResultFailure, err.Error()
	} else if code != 0 {
		out.result, out.err = runfile.ResultFailure, exitError("rclone", code, last.get())
	}
	return out
}

func (t *rcloneTool) finish(context.Context) error { return nil }

func (t *rcloneTool) cleanup() {
	if t.conf != "" {
		os.Remove(t.conf)
	}
}

// ---- restic ----

// restic's exit codes that mean something to the run.
const (
	resticPartial       = 3  // a snapshot was made, some files could not be read
	resticNoRepository  = 10 // nothing at the repository location
	resticWrongPassword = 12
)

type resticTool struct {
	r      *runner
	dest   runfile.Destination
	bin    string
	pwFile string
	env    []string
	lists  []string
}

func (t *resticTool) prepare(ctx context.Context, checkOnly bool) (string, error) {
	bin, err := toolPath("restic", "RESTIC_BIN", resticBin)
	if err != nil {
		return "", err
	}
	t.bin = bin
	t.pwFile = filepath.Join(t.r.spec.WorkDir, fmt.Sprintf("dest-%d.restic.key", t.dest.ID))
	if err := os.WriteFile(t.pwFile, []byte(t.dest.Password), 0o600); err != nil {
		return "", fmt.Errorf("write restic password file: %w", err)
	}
	// Credentials travel in the environment of restic alone, never in argv.
	for k, v := range t.dest.Env {
		t.env = append(t.env, k+"="+v)
	}
	t.env = append(t.env, "RESTIC_REPOSITORY="+t.dest.Repository, "RESTIC_PASSWORD_FILE="+t.pwFile)
	if t.r.spec.CacheDir != "" {
		t.env = append(t.env, "RESTIC_CACHE_DIR="+t.r.spec.CacheDir)
	}

	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	code, last, err := t.run(cctx, []string{"cat", "config", "--json"}, nil, true)
	switch {
	case err != nil:
		return "", err
	case cctx.Err() != nil && ctx.Err() == nil:
		return "", fmt.Errorf("no answer from the repository within %s", checkTimeout)
	case code == 0:
		return "", nil
	case code == resticWrongPassword:
		return "", errors.New("wrong password for this repository")
	case code == resticNoRepository && !t.dest.Init:
		return "", errors.New("the repository does not exist")
	case code == resticNoRepository && checkOnly:
		return "the repository does not exist yet; the first run creates it", nil
	case code == resticNoRepository:
		t.r.log.printf("restic: creating the repository")
		code, last, err = t.run(ctx, []string{"init", "--json"}, nil, false)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return "", fmt.Errorf("could not create the repository: %s", exitError("restic", code, last))
		}
		return "", nil
	}
	return "", errors.New(exitError("restic", code, last))
}

// resticLine is one of restic's JSON messages.
type resticLine struct {
	MessageType string `json:"message_type"`

	// status
	TotalFiles int64 `json:"total_files"`
	FilesDone  int64 `json:"files_done"`
	TotalBytes int64 `json:"total_bytes"`
	BytesDone  int64 `json:"bytes_done"`

	// summary
	FilesNew     int64 `json:"files_new"`
	FilesChanged int64 `json:"files_changed"`
	DataAdded    int64 `json:"data_added"`

	// error, exit_error
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
	Item string `json:"item"`
}

// run runs restic and returns its exit code and last error line. onMsg sees
// status and summary messages. quiet keeps the output of a command whose
// answer is its exit code (and whose stdout is the repository's config) out of
// the journal.
func (t *resticTool) run(ctx context.Context, args []string, onMsg func(resticLine), quiet bool) (int, string, error) {
	// last is the newest error restic reported; exit is its closing line,
	// which only repeats the exit code in words when an error came first.
	var last, exit lastError
	code, err := runTool(ctx, t.bin, args, t.env, func(line []byte) {
		var m resticLine
		if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &m) != nil {
			if strings.HasPrefix(string(line), "Fatal:") {
				exit.set(string(line))
			}
			if !quiet {
				t.r.log.printf("restic: %s", line)
			}
			return
		}
		switch m.MessageType {
		case "status", "summary":
			if onMsg != nil {
				onMsg(m)
			}
			if m.MessageType == "summary" {
				t.r.log.printf("restic: %s", line)
			}
		case "error":
			msg := m.Error.Message
			if m.Item != "" {
				msg = m.Item + ": " + msg
			}
			last.set(msg)
			t.r.log.printf("restic: error: %s", msg)
		case "exit_error":
			exit.set(m.Message)
			if !quiet {
				t.r.log.printf("restic: %s", m.Message)
			}
		default:
			if !quiet {
				t.r.log.printf("restic: %s", line)
			}
		}
	})
	msg := last.get()
	if msg == "" {
		msg = exit.get()
	}
	return code, msg, err
}

func resticTag(backup string) string {
	return "backup=" + strings.NewReplacer(",", "_", "\n", "_").Replace(backup)
}

func (t *resticTool) args(f runfile.Folder) ([]string, error) {
	args := []string{"backup", "--json", "--one-file-system", "--tag", resticTag(t.r.spec.Backup), "--retry-lock", "5m"}
	if kib, ok := bwlimitKiB(t.r.spec.Bwlimit); ok {
		args = append(args, "--limit-upload", strconv.FormatInt(kib, 10))
	} else if t.r.spec.Bwlimit != "" {
		t.r.log.printf("restic: speed limit %q cannot be expressed for restic, running without", t.r.spec.Bwlimit)
	}
	for _, p := range f.Excludes {
		// restic anchors a pattern at the filesystem root, not at the folder.
		if strings.HasPrefix(p, "/") {
			p = strings.TrimSuffix(f.Path, "/") + p
		}
		args = append(args, "--exclude", p)
	}
	if len(f.Files) == 0 {
		return append(args, f.Path), nil
	}
	files, err := expandFiles(f.Path, f.Files)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("nothing in %s matches what this item copies", f.Path)
	}
	list := filepath.Join(t.r.spec.WorkDir, fmt.Sprintf("dest-%d.files.%d", t.dest.ID, len(t.lists)))
	if err := os.WriteFile(list, []byte(strings.Join(files, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	t.lists = append(t.lists, list)
	return append(args, "--files-from-verbatim", list), nil
}

func (t *resticTool) copy(ctx context.Context, f runfile.Folder) outcome {
	args, err := t.args(f)
	if err != nil {
		return failed("%s", err)
	}
	var (
		mu       sync.Mutex
		out      = outcome{result: runfile.ResultSuccess}
		lastAt   time.Time
		lastDone int64
		speed    float64
	)
	dest := t.dest.Name
	code, last, err := t.run(ctx, args, func(m resticLine) {
		if m.MessageType == "summary" {
			mu.Lock()
			out.files, out.bytes = m.FilesNew+m.FilesChanged, m.DataAdded
			mu.Unlock()
			return
		}
		// restic reports no speed; work it out from what it read since last time.
		now := time.Now()
		if !lastAt.IsZero() {
			if dt := now.Sub(lastAt).Seconds(); dt >= 1 {
				speed = float64(m.BytesDone-lastDone) / dt
				lastAt, lastDone = now, m.BytesDone
			}
		} else {
			lastAt, lastDone = now, m.BytesDone
		}
		sp := speed
		t.r.track.progress(func(st *runfile.State) {
			st.Progress = &runfile.Progress{
				Destination: dest, Folder: f.Label,
				BytesDone: m.BytesDone, BytesTotal: m.TotalBytes,
				FilesDone: m.FilesDone, FilesTotal: m.TotalFiles, Speed: sp,
			}
		})
	}, false)
	mu.Lock()
	defer mu.Unlock()
	switch {
	case err != nil:
		out.result, out.err = runfile.ResultFailure, err.Error()
	case code == resticPartial:
		out.result = runfile.ResultPartial
		out.err = "some files could not be read"
		if last != "" && !strings.Contains(last, "could not be read") {
			out.err += "; the last one: " + last
		}
	case code != 0:
		out.result, out.err = runfile.ResultFailure, exitError("restic", code, last)
	}
	return out
}

// finish removes the snapshots the keep rule no longer wants. All zero keeps
// everything, and restic refuses a forget without a rule.
func (t *resticTool) finish(ctx context.Context) error {
	k := t.dest.Keep
	args := []string{"forget"}
	for _, kv := range []struct {
		flag string
		n    int
	}{{"--keep-daily", k.Daily}, {"--keep-weekly", k.Weekly}, {"--keep-monthly", k.Monthly}, {"--keep-yearly", k.Yearly}} {
		if kv.n > 0 {
			args = append(args, kv.flag, strconv.Itoa(kv.n))
		}
	}
	if len(args) == 1 {
		return nil
	}
	args = append(args, "--group-by", "host,paths", "--tag", resticTag(t.r.spec.Backup), "--prune", "--retry-lock", "5m")
	code, last, err := t.run(ctx, args, nil, false)
	if err != nil {
		return fmt.Errorf("remove old snapshots: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("remove old snapshots: %s", exitError("restic", code, last))
	}
	return nil
}

func (t *resticTool) cleanup() {
	if t.pwFile != "" {
		os.Remove(t.pwFile)
	}
	for _, l := range t.lists {
		os.Remove(l)
	}
}
