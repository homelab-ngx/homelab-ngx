# Contributing

How to build, test and change homelab-ngx. `README.md` says what it is for
users.

## Layout

```
apps/core/          # Go daemon + CLI: cmd/ internal/ db/; api/ holds the emitted openapi.json
apps/backup-runner/ # the program a backup run starts: its own Go module, standard library only; runfile/ is its contract with the daemon
apps/dashboard/     # Svelte dashboard (SvelteKit SPA)
image/              # base/ is the mkosi definition (Arch), core/ the core extension's files
nix/                # flake outputs, dev shells, pkgs/ (the packages and the core extension)
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
- For a command no target covers, use the shell from `apps/core`:
  `nix develop -c go test ./internal/x -run TestY`.
- The shell carries the repo's Go, sqlc, Node and the cgo toolchain `-race`
  needs.
- `make verify-changed` runs the checks the changed files call for.
  `make verify` runs all of them.
- `make image-check` boots the image in VMs and checks it end to end. Run it
  for anything that changes the image or the daemon's start.
- `image/build.sh` reads Arch snapshot packages from `https://dl.homelab-ngx.com/arch`
  (`HOMELAB_ARCH_MIRROR`) with fallback to `archive.archlinux.org`; `scripts/sync-arch-mirror`
  populates the mirror.
- `scripts/run -- <cmd>` runs a long command and keeps its full output in a
  log under `~/.local/state/homelab/runs/`.
- `make image` and `make image-check` are heavy. `scripts/builder <target>`
  runs them on another machine over SSH.
- They refuse to run on a host that runs homelab-daemon, unless
  `/var/lib/homelab/build-machine` marks it as kept for building.
- The builder's settings are in `~/.config/homelab/builder.env`.

## Changing the code

- **The API is code-first (Huma).** An operation is a handler with its own Go
  types.
- Add it with `httpx.Register` at its scope (operator, read or control) in
  the feature package's `http.go`.
- Errors are `*httpx.Problem` (problem+json).
- **Schema changes** go in `apps/core/db/{migrations,queries}`. `metrics.db`
  is the one exception: its single table is created by `internal/metrics`.
- **After changing an operation or the SQL**, run `make gen` and commit its
  output with the change. `make gen-check` fails on drift.
- The output is `api/openapi.json`, `schema.gen.ts` and the sqlc `*.gen.go`.
- **Dashboard copy and type** follow these rules:
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
  as a successful local operation.
- Pipe secrets with `--value-stdin`; never put secret values in argv.

**A core runs on older images.** Image and core update apart, so a new core
meets hosts whose image is older. When the daemon uses something of the
image (a program, a unit, a file), it treats its absence as that feature
being unavailable and still starts. When it cannot do without, raise
`minImage` in `nix/pkgs/core-extension.nix` to the image version that brings
it: hosts then apply that core only on such an image.

## Writing

This covers the README, the docs, issues and commit messages (the release
notes are made from them). Dashboard copy has its own, stricter rules, under
"Changing the code".

The reader runs a home server and has never seen this repo. Write to them
plainly:

- **Short sentences that state a fact.** "You" is the reader. "We" is the
  project, and only where the project made a choice.
- **What is true now, in the present tense.** Planned work goes under
  Roadmap and nowhere else.
- **A fact behind every adjective.** "Small" is followed by the size. When
  there is no fact, the adjective goes.
- **The limit next to the claim.** If a feature stops somewhere, the same
  paragraph says where.
- **One word for one thing.** A stack is always a stack.
- **Say it once.** A paragraph ends when its last fact is stated.
- **Headings name their subject** in a few words, in sentence case.
- **A list item is 23 words at most**, in a bulleted or a numbered list.
  What needs more is a paragraph.

Compare:

> homelab-ngx offers powerful, seamless updates that just work.

> An update is written to the other root slot. If it does not boot, the host
> starts the slot before.

> Stacks aren't just containers, they're a whole new way to run services.

> A stack is a repo with a flake and a manifest. It runs as its own NixOS
> system in a systemd-nspawn container.

The first of each pair could describe any project. These habits make text
read that way, so leave them out:

- praise without a measure: seamless, powerful, robust, modern, simply,
  effortless, "just works", "the best";
- "not just X, but Y", and three items in a row where the facts gave two;
- a last line that repeats the paragraph, or an opening that announces it;
- several hedges on one claim: say it, or say what is unknown;
- em dashes, emoji, exclamation marks, and a question as a heading.

### Commit subjects

A subject is `area: what is now true`, in lowercase, as a statement:

```
installer: an existing ESP needs 400 MB free
updates: no restart loop on a good version
```

The release notes are made from these subjects, so write each one for
someone who runs the OS and does not read the code. Every subject has an
area. The areas are `installer`, `image`, `updates`, `daemon`, `dashboard`,
`stacks`, `drafts`, `storage`, `backups` and `notifications`, and for work a
user never meets: `ci`, `image check`, `docs` and `CONTRIBUTING`. The notes
fold that second group under "Upkeep".

### Screenshots

The README's screenshots are in `docs/images`, each page in a light and a
dark file. `make screenshots` takes them from the demo, so they show a
made-up host. Run it after a change to a page they show.

### The demo

`make demo-serve` builds the dashboard in demo mode and serves it. In that
build a made-up host in the browser answers every call
(`apps/dashboard/src/demo`), and no other build contains that code. A push
to main publishes it (`.github/workflows/demo.yml`).

- `fetch.ts` has one handler for each GET in the daemon's API. The type
  check fails when the API changes and the demo did not.
- `host.ts` holds the host and the changes the demo acts out. Any other
  change answers `not_in_demo`.
- `make demo-check` loads every page of the demo in a browser and fails on
  an error.

## The image check

`make image-check` boots the images in `image/out` in QEMU and checks them in
19 steps. It is a Go test in `apps/core/internal/imagecheck`, behind the build
tag `imagecheck`.

It uses the newest three versions in `image/out`, or the ones `VERSIONS`
names. The first is installed, the second is the update to it, and the third
is installed broken to see the host fall back. With one version, steps 9 to 11
are left out.

The steps run in five scenarios. A scenario starts from fresh disks, or from
the snapshot another one saved: its disks, powered off, and the firmware's
variables.

| Scenario | Steps | Starts from |
| --- | --- | --- |
| `installer` | 1-3 | fresh disks; saves `installed` |
| `host` | 4-8 | `installed`; saves `configured` |
| `updates` | 9-12 | `configured` |
| `beside` | 13 | `installed` |
| `under` | 14-19 | fresh disks and firmware |

Make variables change a run:

- `ONLY=updates` runs that scenario, and first the ones whose snapshots it
  needs.
- `RESUME=1` keeps the snapshots, and starts from one made from the same image
  files.
- `JOBS=2` runs two scenarios at once, with twice the memory.
- `KEEP=1` leaves a failed scenario's VM running and prints the SSH command.
- `BUILD=1` adds step 12: the host builds an update from this commit, twice.
- `CPUS` and `MEM` (MiB) size each VM: 4 and 4096, or 6144 with `BUILD=1`.
- `NOVSOCK=1` is for a host without `/dev/vhost-vsock`: SSH then goes over a
  forwarded port.

The check reaches each VM's sshd over vsock, and counts the VM's boots by
QEMU's reset events. A boot is up when its SSH answers. A failed scenario
leaves its logs in `image/out/vm/logs/<scenario>`: a serial log per boot,
QEMU's log with the VM's events, and the VM's journal.

On a build machine with 16 cores (`CPUS=8 MEM=8192 JOBS=2`) a full run with
`BUILD=1` takes 23 minutes, 12 of them in step 11.

A release from before a feature has that part of a step left out, and the
last line lists what was. On the version under test, a missing feature fails
the run.

## Releasing

A tag `vX.Y.Z` on main publishes a release (`.github/workflows/release.yml`).
Its check boots the new version alone (steps 1-8 and 13-19). It does not
check the update from the release before or the fall back from a broken image
(steps 9-11).

So before you tag, the full check must have passed on the commit you tag.
Run it one of two ways:

- on the build machine: `scripts/builder image-check`, with three versions
  in its `image/out` (see [The image check](#the-image-check));
- in Actions: **image check** > Run workflow, on main. It updates from the
  newest published release, and publishes nothing.

On the build machine the third version is only installed and broken, so
`make image-variant FROM=<second> VERSION=<third>` copies it from the second
in seconds.

The image check workflow also runs on main twice a week. Those runs fill
the build caches a release reads.

When a release run fails, run it again with **Re-run failed jobs**; never
move the tag or take the next number for a failure of the run itself. The
`release` job builds, checks and uploads the version, and `announce` lists
it, writes the GitHub Release and moves the installer link. Once the version
is uploaded only `announce` runs again, and each of its steps can run twice.
To announce an uploaded version outside its run, start the workflow by hand
with the version.

Nobody writes the release notes. `scripts/release-notes X.Y.Z` makes them
from the commit subjects since the release before and from the two
releases' package lists, and the workflow publishes what it prints. Run it
with `--to main` before tagging to read the notes the tag would get; a
subject that reads badly there is fixed in the next commit's subject, not in
the notes. The workflow also publishes `/changelog.json`
(`scripts/release-notes --changelog-json`) for the dashboard's Updates page
and post-upgrade dialog.

## Knowledge

When a solved problem's reasoning can't be recovered from the code or the
tests, write it as a comment at the code it explains, in the same change.

Docs live in `README.md` and this file.

## Agent tools

The repo holds no agent instructions or tool settings. `AGENTS.md`,
`CLAUDE.md`, `.claude/` and their kind are gitignored, so keep yours outside
the repo or uncommitted.

`image/core/usr/share/homelab/agent` is product, not instructions for
contributors. The core ships it to the agents a user runs on the host, and
`apps/core/internal/hostcfg/agents.go` links it into their config under
`/etc`. Change it when a `homelab` command or a log's place changes. The
stack rules and the template it ships come from `apps/core/internal/drafts`.
