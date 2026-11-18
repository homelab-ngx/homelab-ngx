package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v2"

	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/stacks"
)

// stacksBuildCommand builds a version 7 or later stack here, as the daemon would,
// without the daemon: a check that a repo builds before it is installed.
func stacksBuildCommand() *cli.Command {
	return &cli.Command{
		Name:      "build",
		Usage:     "Build a stack the way the daemon would, here and as you; nothing is installed (manifest version 7 or later)",
		ArgsUsage: "[--name <name>] [--nixpkgs <channel|commit>] <folder|source>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name", Usage: "the stack name to build for (its hostname); default the manifest's"},
			&cli.StringFlag{Name: "nixpkgs", Usage: "build with this nixpkgs instead of the manifest's (nixos-unstable, nixos-YY.MM or a commit)"},
		},
		Action: func(c *cli.Context) error {
			if err := requireArgs(c, 1, "homelab stacks build [--name <name>] [--nixpkgs <channel|commit>] <folder|source>"); err != nil {
				return err
			}
			path, err := buildStack(c.Context, c.Args().First(), c.String("name"), c.String("nixpkgs"))
			if err != nil {
				return err
			}
			fmt.Println(path)
			return nil
		},
	}
}

// buildStack builds the stack in source (a folder with its uncommitted
// changes, or a flake reference) the way the daemon would and returns the
// system's store path. nix's progress and errors go to stderr.
func buildStack(ctx context.Context, source, name, nixpkgs string) (string, error) {
	ref := source
	if st, err := os.Stat(source); err == nil && st.IsDir() {
		abs, err := filepath.Abs(source)
		if err != nil {
			return "", err
		}
		ref = "path:" + abs
	}
	// This host's daemon config, when it has one, gives the GPU drivers and
	// the domain route labels are served under. A build needs a domain only
	// for a version 8 route label to read; any one will do.
	var gpu stacks.GPU
	domain := "example.invalid"
	if n, err := config.LoadNix("/etc/homelab-daemon/config.json"); err == nil {
		gpu = stacks.GPU{Packages: n.Stacks.GPU.Packages, LibvaDriver: n.Stacks.GPU.LibvaDriver}
		if n.Stacks.Domain != "" {
			domain = n.Stacks.Domain
		}
	}
	stacks.SetPrivateDomain(domain)
	return stacks.FileBuild{
		Runner: cmdrunner.OS{}, Ref: ref, Name: name, Nixpkgs: nixpkgs, GPU: gpu, TimeZone: stacks.HostTimeZone(),
		OnLine: func(line string) { fmt.Fprintln(os.Stderr, line) },
		OnStart: func(name, spec, rev string) {
			fmt.Fprintf(os.Stderr, "Building %s as %q with nixpkgs %s (%s)\n", source, name, rev[:12], spec)
		},
	}.Run(ctx)
}
