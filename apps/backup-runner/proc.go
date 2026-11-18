package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// maxLine is how much of one output line is kept. rclone's stats lines grow
// with the number of transfers; anything longer than this is cut, which makes
// it unparseable and sends its head to the journal.
const maxLine = 256 << 10

// killAfter is how long a child gets to stop after SIGTERM before SIGKILL.
var killAfter = 30 * time.Second

// errNotStarted wraps a failure to start the tool at all.
var errNotStarted = errors.New("could not start")

// runTool runs bin with args and env added to the runner's own environment,
// and hands every line of its stdout and stderr to onLine in order. It
// returns the exit code. When ctx ends the child's process group gets
// SIGTERM and is awaited.
func runTool(ctx context.Context, bin string, args, env []string, onLine func([]byte)) (int, error) {
	rd, wr, err := os.Pipe()
	if err != nil {
		return -1, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = wr, wr
	// Its own process group: a signal to the runner reaches the child only
	// through Cancel, and reaches the helpers the child started too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = killAfter
	if err := cmd.Start(); err != nil {
		rd.Close()
		wr.Close()
		return -1, errors.Join(errNotStarted, err)
	}
	wr.Close()

	read := make(chan struct{})
	go func() {
		defer close(read)
		readLines(rd, maxLine, onLine)
	}()
	werr := cmd.Wait()
	// A helper the child left behind can hold the pipe open; do not wait for it.
	select {
	case <-read:
	case <-time.After(2 * time.Second):
	}
	rd.Close()
	<-read

	var ee *exec.ExitError
	switch {
	case werr == nil:
		return 0, nil
	case errors.As(werr, &ee):
		return ee.ExitCode(), nil // -1 when a signal ended it
	default:
		return -1, werr
	}
}

// readLines calls fn for every line of r, without the line ending. A line of
// any length is read to its end, but only its first max bytes are kept.
func readLines(r io.Reader, max int, fn func([]byte)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	for {
		chunk, more, err := br.ReadLine()
		if room := max - len(line); room > 0 && len(chunk) > 0 {
			if len(chunk) > room {
				chunk = chunk[:room]
			}
			line = append(line, chunk...)
		}
		if err != nil {
			if len(line) > 0 {
				fn(line)
			}
			return
		}
		if !more {
			if len(line) > 0 {
				fn(line)
			}
			line = line[:0]
		}
	}
}
