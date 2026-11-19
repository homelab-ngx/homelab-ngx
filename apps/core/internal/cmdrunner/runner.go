package cmdrunner

import (
	"context"
	"io"
)

// Runner is the seam feature packages depend on instead of calling
// cmdrunner.New directly, so tests can substitute cmdrunnertest.Fake.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// Cmd describes one command for Runner.Run. Module must be a canonical
// module name (see canonicalModules).
type Cmd struct {
	Module string
	Name   string
	Args   []string
	Env    []string // KEY=value, appended to the process environment
	Stdin  io.Reader
	Cwd    string
	AsUser string
	Output OutputMode
	// OnLine, when set, receives every stdout/stderr line as it is produced.
	OnLine func(stream, line string)
	// ExpectExit lists non-zero exit codes logged at DEBUG instead of WARN
	// (Builder.ExpectExit).
	ExpectExit []int
}

// OS runs commands on the host through Builder.
type OS struct{}

// Run executes c. The returned error is a *Error for a failed command.
func (OS) Run(ctx context.Context, c Cmd) (Result, error) {
	b := New(c.Module, c.Name, c.Args...).WithContext(ctx).Output(c.Output)
	if c.AsUser != "" {
		b.AsUser(c.AsUser)
	}
	if c.Cwd != "" {
		b.WithCwd(c.Cwd)
	}
	if len(c.Env) > 0 {
		b.WithEnv(c.Env...)
	}
	if c.Stdin != nil {
		b.WithStdin(c.Stdin)
	}
	if c.OnLine != nil {
		b.WithLineHandler(c.OnLine)
	}
	if len(c.ExpectExit) > 0 {
		b.ExpectExit(c.ExpectExit...)
	}
	return b.Run()
}

var _ Runner = OS{}
