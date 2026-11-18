package cmdrunner_test

import (
	"context"
	"strings"
	"testing"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/cmdrunner/cmdrunnertest"
)

func TestOSRunsAndResolvesSecretsPerInstance(t *testing.T) {
	t.Parallel()
	o := cmdrunner.OS{Secrets: func(name string) string { return "MY_SECRET=" + name + "-value" }}
	res, err := o.Run(context.Background(), cmdrunner.Cmd{Module: "api", Name: "printenv", Args: []string{"MY_SECRET"}, Secrets: []string{"tok"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "tok-value" {
		t.Fatalf("stdout %q", res.Stdout)
	}
	// An OS without a resolver must not see the (nil) package resolver's secrets.
	res, err = cmdrunner.OS{}.Run(context.Background(), cmdrunner.Cmd{Module: "api", Name: "echo", Args: []string{"hi"}, Secrets: []string{"tok"}})
	if err != nil || strings.TrimSpace(res.Stdout) != "hi" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

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
