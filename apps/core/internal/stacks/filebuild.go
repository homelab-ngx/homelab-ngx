package stacks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"homelab-ngx.com/core/internal/cmdrunner"
)

// CheckManifest reads a manifest the way an install would under the name
// installName: it parses and validates it, refuses a version that is no
// longer built, and refuses health units the host would show under one name.
func CheckManifest(raw []byte, installName string) (Manifest, error) {
	m, err := ParseManifest(raw)
	if err != nil {
		return Manifest{}, err
	}
	if m.Version < MinBuildVersion {
		return Manifest{}, fmt.Errorf("version: %d is no longer built; use version %d or later", m.Version, MinBuildVersion)
	}
	if installName != "" {
		if err := hostUnitsClash(installName, m); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// placeholderID is the uid or gid a stack's user or group takes in a build
// outside an install, when its folder is not on this machine or root owns
// it: the build only has to work, an install uses the real ids.
const placeholderID = 65000

// PlaceholderPaths are the host folders a build outside an install gives a
// stack's path variables: each one's default, else "" (a placeholder).
func PlaceholderPaths(m Manifest) map[string]string {
	paths := map[string]string{}
	for _, v := range m.pathVars() {
		paths[v.Name] = ""
		if v.Default != nil {
			paths[v.Name] = *v.Default
		}
	}
	return paths
}

// PlaceholderOwner reads who owns a folder for a build outside an install:
// the folder's owner when it exists and is not root, else placeholderID.
func PlaceholderOwner(path string) (uid, gid uint32, err error) {
	uid, gid = placeholderID, placeholderID
	if path == "" {
		return uid, gid, nil
	}
	if u, g, err := statOwner(path); err == nil {
		if u != 0 {
			uid = u
		}
		if g != 0 {
			gid = g
		}
	}
	return uid, gid, nil
}

// FileBuild builds a stack's files outside an install, the way an install
// builds them: `homelab stacks build` and a draft's Build. Nothing is
// installed or started.
type FileBuild struct {
	// Runner runs nix and git.
	Runner cmdrunner.Runner
	// Ref is the flake reference of the files: a path:, git+file:// or
	// remote reference. It is copied into the store first.
	Ref string
	// Name is the stack name to build for (its hostname); "" is the
	// manifest's.
	Name string
	// Nixpkgs is a channel or commit; "" is the manifest's.
	Nixpkgs string
	// GPU is the host's graphics stack, used with the GPU grant.
	GPU GPU
	// TimeZone is the host's; "" leaves NixOS's default.
	TimeZone string
	// OnLine gets every line nix prints while it builds; nil drops them.
	OnLine func(string)
	// OnStart, when set, is told what is about to be built.
	OnStart func(name, nixpkgs, rev string)
}

// Run copies the files into the store, reads their manifest, writes the
// daemon's wrapper flake and builds it. It returns the system's store path.
func (b FileBuild) Run(ctx context.Context) (string, error) {
	f, err := prefetch(ctx, b.Runner, b.Ref)
	if err != nil {
		return "", fmt.Errorf("copying the files: %w", err)
	}
	if !strings.HasPrefix(f.StorePath, "/nix/store/") || f.Hash == "" {
		return "", errors.New("copying the files: nix gave no store path")
	}
	raw, err := os.ReadFile(filepath.Join(f.StorePath, ManifestFile))
	if err != nil {
		return "", fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if _, err := os.Stat(filepath.Join(f.StorePath, FlakeFile)); err != nil {
		return "", fmt.Errorf("%s: %w", FlakeFile, err)
	}
	name := b.Name
	m, err := CheckManifest(raw, name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ManifestFile, err)
	}
	if name == "" {
		name = m.Name
	}
	spec := b.Nixpkgs
	if spec == "" {
		spec = m.Nixpkgs
	}
	rev, err := ResolveNixpkgs(ctx, b.Runner, spec)
	if err != nil {
		return "", err
	}
	host, _, err := BuildHostModule(m, PlaceholderPaths(m), PlaceholderOwner, b.GPU)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "homelab-stack-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	w := Wrapper{
		Name: name, NixpkgsRev: rev, TimeZone: b.TimeZone, Host: host,
		Ref: "path:" + f.StorePath + "?narHash=" + url.QueryEscape(f.Hash),
	}
	if err := w.Write(dir); err != nil {
		return "", err
	}
	if b.OnStart != nil {
		b.OnStart(name, spec, rev)
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	res, err := b.Runner.Run(ctx, cmdrunner.Cmd{
		Module: "stacks", Name: "nix",
		Args: []string{"--extra-experimental-features", "nix-command flakes",
			"build", "--no-link", "--print-out-paths", "--print-build-logs", "--no-write-lock-file",
			"--", "path:" + dir + "#" + WrapperSystem},
		OnLine: func(stream, line string) {
			if b.OnLine != nil && stream != "stdout" {
				b.OnLine(line)
			}
		},
	})
	if err != nil {
		// nix's error is in the lines OnLine was given; the command's own
		// error repeats all of it.
		var ce *cmdrunner.Error
		if errors.As(err, &ce) && ce.Result.ExitCode > 0 {
			return "", fmt.Errorf("nix build failed (exit %d)", ce.Result.ExitCode)
		}
		return "", fmt.Errorf("nix build: %w", err)
	}
	return buildPath(res.Stdout)
}
