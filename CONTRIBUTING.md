# Contributing

How to build, test and change homelab-ngx. `README.md` says what it is for
users. `STRATEGY.md` gives the direction. `CONTEXT.md` is the domain glossary:
use its terms in code, issues and docs.

## Layout

```
apps/core/          # Go daemon + CLI: cmd/ internal/ db/; api/ holds the emitted openapi.json
apps/backup-runner/ # the program a backup run starts: its own Go module, standard library only; runfile/ is its contract with the daemon
apps/dashboard/     # Svelte dashboard (SvelteKit SPA); PRODUCT.md + DESIGN.md hold its design context
image/              # base/ is the mkosi definition (Arch), core/ the core extension's files
nix/                # flake outputs, dev shells, pkgs/ (the packages and the core extension)
docs/solutions/     # why things are shaped this way
```

There are two Go modules:
- `homelab-ngx.com/core` (`apps/core`);
- `homelab-ngx.com/core/backup-runner` (`apps/backup-runner`, with its own
  `go.mod`).

Never add a `backup-runner` directory inside `apps/core`: Go would also look
for that package in the core module.

## Build and test

Work from the repo root. You need Nix with flakes. The dev shells run on
x86_64 Linux and on Apple silicon Macs; the image builds only on Linux.

- `make` lists the targets. The Makefile enters the Nix dev shell itself, so
  don't wrap it in `nix develop`.
- Scope a test run:
  - `make daemon-test PKG=./internal/x RUN=TestY`
  - `make dashboard-test FILE=src/lib/x.test.ts`
- For a command no target covers, run it in the shell:
  `nix develop -c go test ./internal/x -run TestY` (from `apps/core`). The
  shell carries the repo's Go, sqlc, Node and the cgo toolchain `-race`
  needs.
- `make verify-changed` runs the checks the changed files call for, and
  `make verify` runs all of them. For anything that changes the image or the
  daemon's start, `make image-check` boots the image in a VM.
- `scripts/run -- <cmd>` runs a long command with its full output kept in a
  log under `~/.local/state/homelab/runs/`. It prints a heartbeat and a footer
  with the exit code.
- `make image` and `make image-check` are heavy. They refuse to run on a
  machine that runs homelab-daemon, unless `/var/lib/homelab/build-machine`
  exists there: a host kept for building is marked that way. `scripts/builder
  <target>` runs them on another machine over SSH, with its settings in
  `~/.config/homelab/builder.env`.

## Changing the code

- **The API is code-first (Huma).** An operation is a handler with its own Go
  types, added with `httpx.Register` at its scope (operator, read or
  control) in the feature package's `http.go`. Errors are `*httpx.Problem`
  (problem+json).
- **Schema changes** go in `apps/core/db/{migrations,queries}`. `metrics.db`
  is the one exception: its single table is created by `internal/metrics`.
- **After changing an operation or the SQL**, run `make gen`. Commit its
  output with the change: `api/openapi.json`, `schema.gen.ts` and the sqlc
  `*.gen.go`. `make gen-check` fails on drift.
- **Dashboard copy and type** follow `apps/dashboard/DESIGN.md` (Copy,
  Help, Status, Row Menu, Disclosure):
  - labels and values instead of sentences;
  - explanations in the label's `Help`;
  - one font family.

  `src/ui-rules.test.ts` fails when a page grows past its word limits or
  uses a font size, mono or a fold outside the system. Cut the text instead
  of raising a limit. The dashboard is Svelte 5 with runes.
- **Dependency hashes.** After changing `go.mod`/`go.sum` or
  `apps/dashboard/package-lock.json`, run
  `scripts/update-hashes.sh daemon|dashboard` to refresh
  `vendorHash`/`npmDepsHash`.
- **CLI automation.** Put `--json` before the command, and treat `warnings`
  as a successful local operation. Pipe secrets with `--value-stdin`; never
  put secret values in argv. Details:
  `docs/solutions/conventions/homelab-cli-automation.md`.

## Releasing

A tag `vX.Y.Z` on main publishes a release (`.github/workflows/release.yml`).
Its check boots the new version alone (`image/check.sh` steps 1-8 and
13-18). It does not check the update from the release before or the fall back
from a broken image (steps 9-11).

So before you tag, the full check must have passed on the commit you tag.
Run it one of two ways:

- on the build machine: `scripts/builder image-check`, with three versions
  in its `image/out` (see `image/check.sh`). The third is only installed and
  broken, so `make image-variant FROM=<second> VERSION=<third>` copies it
  from the second in seconds;
- in Actions: **image check** > Run workflow, on main. It updates from the
  newest published release, and publishes nothing.

The image check workflow also runs on main twice a week. Those runs fill
the build caches a release reads.

## Knowledge

`docs/solutions/` explains:
- the architecture: daemon feature packages, the code-first API, the SQLite
  store, read-only config, doctor, the stack contract and its API grant;
- the image;
- CLI scripting;
- solved problems.

Search it before changing an area it covers. When a solved problem's
reasoning can't be recovered from the code, tests or existing docs, write it
up there with the change.

Docs live in `README.md`, `STRATEGY.md`, `CONTEXT.md`, this file and
`docs/solutions/`.

## Agent tools

The repo holds no agent instructions or tool settings. `AGENTS.md`,
`CLAUDE.md`, `.claude/` and their kind are gitignored, so keep yours outside
the repo or uncommitted.
