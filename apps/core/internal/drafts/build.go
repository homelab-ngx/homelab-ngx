package drafts

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/store"
)

// Check reads the saved files with changes on top, or the saved files alone
// when changes is nil, the way an install would under the draft's name, and
// stores the result on the draft. It parses flake.nix and each changed Nix
// file; a check of the saved files parses every Nix file.
func (s *Service) Check(ctx context.Context, name string, changes *[]DraftChange) (DraftCheck, error) {
	if _, err := s.row(ctx, name); err != nil {
		return DraftCheck{}, err
	}
	c := DraftCheck{CheckedAt: s.now().UTC()}
	saved, err := s.read(name)
	if err != nil {
		return DraftCheck{}, err
	}
	files := saved
	var parse []string
	if changes == nil {
		rev, _, err := s.head(ctx, name)
		if err != nil {
			return DraftCheck{}, err
		}
		c.Saved, c.Rev = true, rev
		for p := range saved {
			parse = append(parse, p)
		}
	} else {
		if files, err = saved.apply(*changes); err != nil {
			return DraftCheck{}, err
		}
		parse = append(saved.changed(files), stacks.FlakeFile)
	}
	c.Problems = CheckFiles(name, files.text(stacks.ManifestFile), files.text(stacks.FlakeFile))
	slices.Sort(parse)
	for _, p := range slices.Compact(parse) {
		f, ok := files[p]
		if !ok || f.Binary || !strings.HasSuffix(p, ".nix") {
			continue
		}
		if prob, ok := s.parseNix(ctx, p, f.Content); !ok {
			c.Problems = append(c.Problems, prob)
		}
	}
	c.OK = len(c.Problems) == 0
	b, _ := json.Marshal(c)
	if err := s.st.Queries().SetDraftCheck(ctx, store.SetDraftCheckParams{Name: name, CheckResult: string(b)}); err != nil {
		return DraftCheck{}, err
	}
	return c, nil
}

// parseNix parses the Nix file at path the way nix reads it, without
// evaluating it: a syntax error is a problem with nix's message and its line.
func (s *Service) parseNix(ctx context.Context, path, text string) (DraftProblem, bool) {
	if strings.TrimSpace(text) == "" {
		return DraftProblem{}, true // empty flake.nix: CheckFiles says so; elsewhere, nix says nothing useful
	}
	res, err := s.r.Run(ctx, cmdrunner.Cmd{
		Module: "drafts", Name: "nix-instantiate", Args: []string{"--parse", "-"},
		Stdin: strings.NewReader(text), ExpectExit: []int{1},
	})
	if err == nil && res.ExitCode == 0 {
		return DraftProblem{}, true
	}
	if res.ExitCode != 1 {
		s.log.Warn("Nix file not parsed", "file", path, "error", err)
		return DraftProblem{}, true // the check cannot say; Build will
	}
	return DraftProblem{File: path, Message: parseError(path, res.Stderr)}, false
}

var parseAt = regexp.MustCompile(`«stdin»:(\d+):(\d+)`)

// parseError is nix's parse error as one line: "syntax error, unexpected
// '{', expecting 'inherit' (line 1, column 2)".
func parseError(path, stderr string) string {
	msg := ""
	for _, l := range strings.Split(ansiRE.ReplaceAllString(stderr, ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			msg = strings.TrimPrefix(l, "error: ")
			break
		}
	}
	if msg == "" {
		msg = path + " is not valid Nix."
	}
	if m := parseAt.FindStringSubmatch(stderr); m != nil {
		msg += fmt.Sprintf(" (line %s, column %s)", m[1], m[2])
	}
	return msg
}

// CheckFiles is what a check finds in a manifest and flake.nix installed as
// name: the manifest's errors as an install would report them, and a
// flake.nix that cannot be the stack's.
func CheckFiles(name, manifest, flake string) []DraftProblem {
	out := []DraftProblem{}
	if strings.TrimSpace(manifest) == "" {
		out = append(out, DraftProblem{File: stacks.ManifestFile, Message: "homelab-stack.json is empty."})
	} else if _, err := stacks.CheckManifest([]byte(manifest), name); err != nil {
		out = append(out, DraftProblem{File: stacks.ManifestFile, Message: err.Error()})
	}
	switch {
	case strings.TrimSpace(flake) == "":
		out = append(out, DraftProblem{File: stacks.FlakeFile, Message: "flake.nix is empty."})
	case !strings.Contains(flake, "nixosModules"):
		out = append(out, DraftProblem{File: stacks.FlakeFile,
			Message: "flake.nix has to export the stack's module as nixosModules.default."})
	}
	return out
}

// Build builds the saved commit the way an install would, in the
// background. Get shows it while it runs and keeps the result.
func (s *Service) Build(ctx context.Context, name string) (Draft, error) {
	if _, err := s.row(ctx, name); err != nil {
		return Draft{}, err
	}
	rev, _, err := s.head(ctx, name)
	if err != nil {
		return Draft{}, err
	}
	s.mu.Lock()
	if s.builds[name] != nil {
		s.mu.Unlock()
		return Draft{}, httpx.Conflict("build_running", "a build of %q is already running", name)
	}
	b := &running{view: DraftBuild{Rev: rev, StartedAt: s.now().UTC(), Result: BuildRunning}}
	s.builds[name] = b
	s.mu.Unlock()
	s.storeBuild(ctx, name, b.view)
	go s.runBuild(name, rev, b)
	return s.Get(ctx, name)
}

func (s *Service) runBuild(name, rev string, b *running) {
	// The build outlives the request that started it.
	ctx := context.Background()
	line := func(l string) {
		s.mu.Lock()
		b.log.line(l)
		s.mu.Unlock()
	}
	line(fmt.Sprintf("%s Building commit %s of %s", time.Now().Format("15:04:05"), short(rev), name))
	system, err := s.build(ctx, stacks.FileBuild{
		Runner: s.r, Name: name, GPU: s.gpu, TimeZone: stacks.HostTimeZone(),
		Ref:    s.source(name) + "?ref=" + branch + "&rev=" + rev,
		OnLine: line,
		OnStart: func(_, spec, nixpkgs string) {
			line(fmt.Sprintf("%s With nixpkgs %s (%s)", time.Now().Format("15:04:05"), spec, short(nixpkgs)))
		},
	})
	s.mu.Lock()
	v := b.view
	now := s.now().UTC()
	v.FinishedAt = &now
	if err != nil {
		v.Result, v.Error = BuildFailed, nixError(b.log.String(), err)
		b.log.line(fmt.Sprintf("%s Failed: %v", time.Now().Format("15:04:05"), err))
	} else {
		v.Result, v.System = BuildBuilt, system
		b.log.line(fmt.Sprintf("%s Built %s", time.Now().Format("15:04:05"), system))
	}
	v.Log = b.log.String()
	s.mu.Unlock()
	s.storeBuild(ctx, name, v)
	s.mu.Lock()
	delete(s.builds, name)
	s.mu.Unlock()
	s.log.Info("draft built", "draft", name, "rev", rev, "result", v.Result)
}

func (s *Service) storeBuild(ctx context.Context, name string, v DraftBuild) {
	b, _ := json.Marshal(v)
	if err := s.st.Queries().SetDraftBuild(ctx, store.SetDraftBuildParams{Name: name, BuildResult: string(b)}); err != nil {
		s.log.Warn("draft build not stored", "draft", name, "error", err)
	}
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// maxError caps the error a build keeps: enough for every error block of a
// failed evaluation, small enough to hand to the assistant.
const maxError = 16 << 10

// nixError is why a build failed, from its log: nix's error blocks (each
// starts with "error:" and runs to the next line that is not part of it),
// without colours. When nix printed none, it is err.
func nixError(log string, err error) string {
	var b strings.Builder
	in := false
	for l := range strings.SplitSeq(ansiRE.ReplaceAllString(log, ""), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "error:"):
			in = true
		case in && (t == "" || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") || strings.HasPrefix(t, "…") || strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "|")):
		default:
			in = false
		}
		if in {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return err.Error()
	}
	if len(out) > maxError {
		out = out[:maxError] + "\n[… cut …]"
	}
	return out
}

// maxLog caps the log a build keeps. Like a stack attempt's log it keeps
// the head, which names the step, and the tail, which has the error.
const maxLog = 256 << 10

type capLog struct {
	head, tail strings.Builder
	dropped    int
}

func (l *capLog) line(s string) {
	s += "\n"
	if l.head.Len()+len(s) <= maxLog/2 {
		l.head.WriteString(s)
		return
	}
	l.tail.WriteString(s)
	if over := l.tail.Len() - maxLog/2; over > 0 {
		rest := l.tail.String()[over:]
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			rest = rest[i+1:]
		}
		l.dropped += l.tail.Len() - len(rest)
		l.tail.Reset()
		l.tail.WriteString(rest)
	}
}

func (l *capLog) String() string {
	if l.dropped == 0 {
		return l.head.String() + l.tail.String()
	}
	return fmt.Sprintf("%s[… %d bytes left out …]\n%s", l.head.String(), l.dropped, l.tail.String())
}
