// Command homelab-backup-runner copies one backup run's folders to its
// destinations with rclone and restic, and records how far it came.
//
// It is a one-way pipeline: a tool's output is read line by line, each line
// becomes an event, and one goroutine applies the events to the run's state
// and rewrites the state file. Nothing flows back. The daemon starts it in a
// sandboxed transient unit and reads the state file; the runner never talks to
// the daemon or its database.
//
// It uses the standard library only (see TestStandardLibraryOnly).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"homelab-ngx.com/core/backup-runner/runfile"
)

// Exit codes: 0 only when every item succeeded.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// Set at build time (-X) to the tools this build ships with; RCLONE_BIN and
// RESTIC_BIN override them, and PATH is the fallback.
var (
	rcloneBin = ""
	resticBin = ""
)

func main() { os.Exit(realMain(os.Args[1:], os.Stderr)) }

func realMain(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("homelab-backup-runner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	specPath := fs.String("spec", "", "path of the run's spec file (deleted once read)")
	statePath := fs.String("state", "", "path of the state file to write")
	preview := fs.Bool("preview", false, "compare the folders with their destinations and change nothing there")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *specPath == "" || *statePath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: homelab-backup-runner --spec <path> --state <path>")
		return exitUsage
	}
	spec, err := runfile.ReadSpec(*specPath)
	// The spec holds secrets: it goes whether or not it could be read.
	if rmErr := os.Remove(*specPath); rmErr != nil && !os.IsNotExist(rmErr) {
		fmt.Fprintf(stderr, "remove spec: %v\n", rmErr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "read spec: %v\n", err)
		return exitUsage
	}

	// Both the spec and the argument must ask for a preview. A runner older
	// than the mode fails on the argument, and never copies a preview's spec.
	if spec.Preview != *preview || (spec.Preview && spec.CheckOnly) {
		fmt.Fprintln(stderr, "the spec and --preview do not agree")
		return exitUsage
	}
	if spec.CheckOnly {
		spec.Folders = nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	r := &runner{spec: spec, log: &lineLog{w: stderr}, now: time.Now}
	r.track = newTracker(*statePath, initialState(spec, r.now()), r.log)
	result := r.run(ctx)
	if result == runfile.ResultSuccess {
		return exitOK
	}
	return exitFailed
}

// lineLog writes whole lines to the journal from any goroutine.
type lineLog struct {
	mu sync.Mutex
	w  io.Writer
}

// maxLogLine bounds one journal line; a tool can print megabytes on one.
const maxLogLine = 4 << 10

func (l *lineLog) printf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if len(s) > maxLogLine {
		s = s[:maxLogLine] + " …(truncated)"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.w, s)
}

type runner struct {
	spec  *runfile.Spec
	log   *lineLog
	track *tracker
	now   func() time.Time
}

// initialState lists every item as pending, a destination followed by its
// folders, so a reader sees the whole plan from the first write.
func initialState(spec *runfile.Spec, now time.Time) *runfile.State {
	st := &runfile.State{Version: runfile.Version, RunID: spec.RunID, Preview: spec.Preview, StartedAt: now, UpdatedAt: now, Items: []runfile.Item{}}
	for _, d := range spec.Destinations {
		st.Items = append(st.Items, runfile.Item{
			Kind: runfile.ItemDestination, DestinationID: d.ID, Destination: d.Name, Result: runfile.ResultPending,
		})
		for _, f := range spec.Folders {
			st.Items = append(st.Items, runfile.Item{
				Kind: runfile.ItemFolder, DestinationID: d.ID, Destination: d.Name,
				Label: f.Label, Path: f.Path, Note: f.Note, Result: runfile.ResultPending,
			})
		}
	}
	return st
}

// item is the index of destination d's own item (f < 0) or of its folder f.
func (r *runner) item(d, f int) int { return d*(len(r.spec.Folders)+1) + f + 1 }

// run does the whole run and returns its result. A panic anywhere below is
// recorded as the run's error; the state file is final either way.
func (r *runner) run(ctx context.Context) (result string) {
	defer func() {
		if p := recover(); p != nil {
			r.log.printf("panic: %v\n%s", p, debug.Stack())
			result = r.finish(ctx, fmt.Sprintf("runner panic: %v", p))
		}
	}()
	for d := range r.spec.Destinations {
		if ctx.Err() != nil {
			break
		}
		r.destination(ctx, d)
	}
	return r.finish(ctx, "")
}

// finish closes every open item, sets the run's result and writes the last
// state. forced is a run-level error that makes the run a failure.
func (r *runner) finish(ctx context.Context, forced string) string {
	cancelled := ctx.Err() != nil
	now := r.now()
	var result string
	r.track.do(func(st *runfile.State) {
		total, good, bad := 0, 0, 0
		firstErr := ""
		for i := range st.Items {
			it := &st.Items[i]
			if it.Result == runfile.ResultPending || it.Result == runfile.ResultRunning {
				it.Result = runfile.ResultSkipped
				if cancelled && forced == "" {
					it.Result = runfile.ResultCancelled
				}
				if it.Error == "" {
					it.Error = "the run stopped before this was done"
				}
				if it.StartedAt != nil {
					it.FinishedAt = &now
				}
			}
			if it.Result == runfile.ResultSkipped && it.Error == "" {
				continue // left out on purpose (a preview's restic destination)
			}
			total++
			switch it.Result {
			case runfile.ResultSuccess:
				good++
			default:
				bad++
				if firstErr == "" && it.Error != "" {
					firstErr = itemName(it) + ": " + it.Error
				}
			}
		}
		switch {
		case forced != "":
			st.Result, st.Error = runfile.ResultFailure, forced
		case cancelled:
			st.Result, st.Error = runfile.ResultCancelled, "the run was stopped"
		case bad == 0:
			st.Result = runfile.ResultSuccess
		default:
			st.Result = runfile.ResultPartial
			if !anyCopied(st.Items) {
				st.Result = runfile.ResultFailure
			}
			st.Error = fmt.Sprintf("%d of %d items did not succeed; first: %s", bad, total, firstErr)
		}
		st.Progress = nil
		st.FinishedAt = &now
		result = st.Result
	})
	r.track.close()
	return result
}

// anyCopied reports whether at least one folder reached a destination, fully
// or in part. With no folders at all, a reachable destination counts.
func anyCopied(items []runfile.Item) bool {
	folders := false
	for _, it := range items {
		if it.Kind != runfile.ItemFolder {
			continue
		}
		folders = true
		if it.Result == runfile.ResultSuccess || it.Result == runfile.ResultPartial {
			return true
		}
	}
	if folders {
		return false
	}
	for _, it := range items {
		if it.Result == runfile.ResultSuccess {
			return true
		}
	}
	return false
}

func itemName(it *runfile.Item) string {
	if it.Kind == runfile.ItemFolder {
		return it.Label + " → " + it.Destination
	}
	return it.Destination
}

// destination checks one destination, copies every folder to it and, for
// restic, prunes. A destination that cannot be reached skips its folders;
// the next destination still runs.
func (r *runner) destination(ctx context.Context, d int) {
	dest := r.spec.Destinations[d]
	di := r.item(d, -1)
	r.start(di)

	if r.spec.Preview && dest.Kind == runfile.KindRestic {
		const note = "restic destinations are not previewed"
		r.track.do(func(st *runfile.State) {
			st.Items[di].Result, st.Items[di].Note = runfile.ResultSkipped, note
			for f := range r.spec.Folders {
				it := &st.Items[r.item(d, f)]
				it.Result, it.Note = runfile.ResultSkipped, note
			}
		})
		return
	}

	var tool tool
	switch dest.Kind {
	case runfile.KindRclone:
		tool = &rcloneTool{r: r, dest: dest}
	case runfile.KindRestic:
		tool = &resticTool{r: r, dest: dest}
	default:
		r.fail(di, fmt.Sprintf("unknown destination kind %q", dest.Kind))
		r.skipFolders(d, "destination kind is not supported by this runner")
		return
	}
	defer tool.cleanup()

	note, err := tool.prepare(ctx, r.spec.CheckOnly)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.fail(di, err.Error())
		r.skipFolders(d, "the destination could not be reached")
		return
	}
	if r.spec.CheckOnly {
		r.end(di, outcome{result: runfile.ResultSuccess, note: note})
		return
	}

	var files, bytes int64
	for f, folder := range r.spec.Folders {
		if ctx.Err() != nil {
			return
		}
		fi := r.item(d, f)
		r.start(fi)
		out := r.folder(ctx, tool, dest, folder)
		if ctx.Err() != nil {
			return // finish marks it skipped
		}
		files += out.files
		bytes += out.bytes
		r.end(fi, out)
	}

	out := outcome{result: runfile.ResultSuccess, files: files, bytes: bytes}
	if err := tool.finish(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		out.result, out.err = runfile.ResultFailure, err.Error()
	}
	r.end(di, out)
}

// outcome is how one item ended.
type outcome struct {
	result string
	files  int64
	bytes  int64
	err    string
	note   string
	// preview is what a run would do; a preview's folders only.
	preview *runfile.Preview
}

func failed(format string, a ...any) outcome {
	return outcome{result: runfile.ResultFailure, err: fmt.Sprintf(format, a...)}
}

// folder runs the checks that need no tool, then the copy.
func (r *runner) folder(ctx context.Context, t tool, dest runfile.Destination, f runfile.Folder) outcome {
	if f.Fail != "" {
		return failed("%s", f.Fail)
	}
	if err := checkSource(f.Path, r.spec.PoolMounts); err != nil {
		return failed("%s", err)
	}
	if dest.Kind == runfile.KindRclone && dest.DeleteRemoved {
		empty, err := dirEmpty(f.Path)
		if err != nil {
			return failed("%s", err)
		}
		if empty {
			return failed("%s is empty; copying it would delete everything at the destination", f.Path)
		}
	}
	r.track.progress(func(st *runfile.State) {
		st.Progress = &runfile.Progress{Destination: dest.Name, Folder: f.Label}
	})
	if r.spec.Preview {
		rt, ok := t.(*rcloneTool)
		if !ok {
			return failed("this destination kind cannot be previewed")
		}
		return rt.preview(ctx, f)
	}
	return t.copy(ctx, f)
}

func (r *runner) start(i int) {
	now := r.now()
	r.track.do(func(st *runfile.State) {
		st.Items[i].Result = runfile.ResultRunning
		st.Items[i].StartedAt = &now
	})
}

func (r *runner) end(i int, out outcome) {
	now := r.now()
	r.track.do(func(st *runfile.State) {
		it := &st.Items[i]
		it.Result, it.Files, it.Bytes, it.Error = out.result, out.files, out.bytes, out.err
		it.Preview = out.preview
		if out.note != "" {
			it.Note = out.note
		}
		if it.StartedAt == nil {
			it.StartedAt = &now
		}
		it.FinishedAt = &now
		if it.Kind == runfile.ItemFolder {
			st.Progress = nil
		}
	})
}

func (r *runner) fail(i int, msg string) { r.end(i, outcome{result: runfile.ResultFailure, err: msg}) }

func (r *runner) skipFolders(d int, why string) {
	r.track.do(func(st *runfile.State) {
		for f := range r.spec.Folders {
			it := &st.Items[r.item(d, f)]
			it.Result, it.Error = runfile.ResultSkipped, why
		}
	})
}

// tool is one destination kind's way of doing the three steps.
type tool interface {
	// prepare writes the tool's files and checks the destination is reachable.
	// With checkOnly it changes nothing there; note is something worth telling
	// the operator about a destination that is fine.
	prepare(ctx context.Context, checkOnly bool) (note string, err error)
	copy(ctx context.Context, f runfile.Folder) outcome
	// finish runs what follows the folders (restic's prune).
	finish(ctx context.Context) error
	// cleanup removes the files prepare wrote.
	cleanup()
}
