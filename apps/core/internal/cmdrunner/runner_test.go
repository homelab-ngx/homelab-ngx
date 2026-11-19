package cmdrunner_test

import (
	"context"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func TestOSFailureIsError(t *testing.T) {
	t.Parallel()
	_, err := cmdrunner.OS{}.Run(context.Background(), cmdrunner.Cmd{Module: "api", Name: "false"})
	if _, ok := err.(*cmdrunner.Error); !ok {
		t.Fatalf("err = %T %v", err, err)
	}
}

func TestFakePrefixAndSequence(t *testing.T) {
	t.Parallel()
	f := cmdrunnertest.NewFake()
	f.On("systemctl").Reply("generic")
	f.On("systemctl", "show").Reply("one").Reply("two")
	run := func(args ...string) string {
		r, _ := f.Run(context.Background(), cmdrunner.Cmd{Module: "api", Name: "systemctl", Args: args})
		return r.Stdout
	}
	if run("start", "x") != "generic" || run("show", "x") != "one" || run("show", "x") != "two" || run("show", "x") != "two" {
		t.Fatal("longest-prefix / sequence behaviour wrong")
	}
	if _, err := f.Run(context.Background(), cmdrunner.Cmd{Module: "api", Name: "other"}); err == nil {
		t.Fatal("unscripted command must fail")
	}
	if len(f.Calls()) != 5 || len(f.CallsMatching("systemctl", "show")) != 3 {
		t.Fatalf("calls recorded wrong: %d", len(f.Calls()))
	}
}
