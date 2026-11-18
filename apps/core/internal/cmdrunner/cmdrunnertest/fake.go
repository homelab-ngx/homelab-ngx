// Package cmdrunnertest provides a scripted cmdrunner.Runner for tests.
package cmdrunnertest

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// Fake is a cmdrunner.Runner that answers from rules matched by argv prefix
// (Name followed by Args). The longest matching prefix wins; among equal
// lengths the rule registered first wins. A command no rule matches fails
// with exit code 1 and stderr "cmdrunnertest: unscripted command".
type Fake struct {
	mu    sync.Mutex
	rules []*Rule
	calls []cmdrunner.Cmd
}

// NewFake returns an empty Fake.
func NewFake() *Fake { return &Fake{} }

type response struct {
	res cmdrunner.Result
	err error
	fn  func(cmdrunner.Cmd) (cmdrunner.Result, error)
}

// Rule is a scripted answer. Responses are consumed one per call in the order
// they were added; the last one repeats.
type Rule struct {
	prefix []string
	resps  []response
	n      int
}

// On registers a rule for commands whose argv starts with prefix, e.g.
// On("systemctl", "is-active"). Add responses with Reply, Fail or Do.
func (f *Fake) On(prefix ...string) *Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &Rule{prefix: prefix}
	f.rules = append(f.rules, r)
	return r
}

// Reply adds a successful response with the given stdout.
func (r *Rule) Reply(stdout string) *Rule {
	r.resps = append(r.resps, response{res: cmdrunner.Result{Stdout: stdout, Output: stdout}})
	return r
}

// Fail adds a failing response (exit code 1) with the given combined output.
func (r *Rule) Fail(output string) *Rule { return r.FailCode(1, output) }

// FailCode adds a failing response with an explicit exit code.
func (r *Rule) FailCode(code int, output string) *Rule {
	res := cmdrunner.Result{ExitCode: code, Stdout: output, Stderr: output, Output: output}
	r.resps = append(r.resps, response{res: res, err: &cmdrunner.Error{Result: res, Err: errors.New("exit status " + strconv.Itoa(code))}})
	return r
}

// Do adds a response computed from the command (also useful for blocking
// or counting concurrency).
func (r *Rule) Do(fn func(cmdrunner.Cmd) (cmdrunner.Result, error)) *Rule {
	r.resps = append(r.resps, response{fn: fn})
	return r
}

// Run implements cmdrunner.Runner.
func (f *Fake) Run(ctx context.Context, c cmdrunner.Cmd) (cmdrunner.Result, error) {
	if err := ctx.Err(); err != nil {
		return cmdrunner.Result{ExitCode: -1, Cancelled: true}, &cmdrunner.Error{Result: cmdrunner.Result{ExitCode: -1, Cancelled: true}, Err: err}
	}
	argv := append([]string{c.Name}, c.Args...)
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var best *Rule
	for _, r := range f.rules {
		if hasPrefix(argv, r.prefix) && (best == nil || len(r.prefix) > len(best.prefix)) && len(r.resps) > 0 {
			best = r
		}
	}
	var resp response
	if best != nil {
		i := best.n
		if i >= len(best.resps) {
			i = len(best.resps) - 1
		} else {
			best.n++
		}
		resp = best.resps[i]
	}
	f.mu.Unlock()

	switch {
	case best == nil:
		res := cmdrunner.Result{Cmd: c.Name, Args: argv, ExitCode: 1, Stderr: "cmdrunnertest: unscripted command", Output: "cmdrunnertest: unscripted command"}
		return res, &cmdrunner.Error{Result: res, Err: errors.New("exit status 1")}
	case resp.fn != nil:
		return resp.fn(c)
	}
	resp.res.Cmd, resp.res.Args = c.Name, argv
	if c.OnLine != nil {
		for _, l := range strings.Split(strings.TrimRight(resp.res.Stdout, "\n"), "\n") {
			if l != "" {
				c.OnLine("stdout", l)
			}
		}
	}
	return resp.res, resp.err
}

// Calls returns every command run so far, in order.
func (f *Fake) Calls() []cmdrunner.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cmdrunner.Cmd(nil), f.calls...)
}

// CallsMatching returns the commands whose argv starts with prefix.
func (f *Fake) CallsMatching(prefix ...string) []cmdrunner.Cmd {
	var out []cmdrunner.Cmd
	for _, c := range f.Calls() {
		if hasPrefix(append([]string{c.Name}, c.Args...), prefix) {
			out = append(out, c)
		}
	}
	return out
}

func hasPrefix(argv, prefix []string) bool {
	if len(prefix) > len(argv) {
		return false
	}
	for i, p := range prefix {
		if argv[i] != p {
			return false
		}
	}
	return true
}

var _ cmdrunner.Runner = (*Fake)(nil)
