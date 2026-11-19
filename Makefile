# Run `make` (or `make help`) from the repo root. Every target runs inside the
# root dev shell (Go, sqlc, Node, nixfmt): from the host, make re-runs
# itself there once, so callers never wrap it in `nix develop`.
# HOMELAB_DEV_SHELL is exported by that shell only; IN_NIX_SHELL would also
# match the dashboard shell, which has no Go.

ifndef HOMELAB_DEV_SHELL

GOALS := $(or $(MAKECMDGOALS),help)
.PHONY: $(GOALS) _devshell
$(GOALS): _devshell
	@:
# Command-line variables (PKG=, RUN=, FILE=) reach the inner make via MAKEFLAGS.
# The inner make is the dev shell's own by name: on macOS $(MAKE) is the xcrun
# shim, which the shell's Apple SDK points at a make that does not exist.
_devshell:
	@nix develop -c make --no-print-directory $(GOALS)

else

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c
.ONESHELL:
.DEFAULT_GOAL := help
.PHONY: help gen gen-check verify verify-changed daemon-test runner-test dashboard-test dashboard-deps nix-check image image-check image-variant

PKG ?= ./...
BASE ?= HEAD
RUN ?=
FILE ?=

help:
	@cat <<'EOF'
	make verify-changed    only the checks the changed files call for  [BASE=origin/main]
	make verify            every check: gen-check, then the four below (in parallel)
	make daemon-test       gofmt, vet, go test -race, build cmds  [PKG=./internal/x RUN=TestY]
	make runner-test       the backup runner (apps/backup-runner): gofmt, vet, go test -race, static build  [RUN=TestY]
	make dashboard-test    svelte-check, vitest  [FILE=src/lib/x.test.ts, relative to apps/dashboard]
	make dashboard-deps    npm ci in apps/dashboard (after package-lock.json changes)
	make nix-check         nix fmt --fail-on-change, nix flake check --no-build
	make image             build the image host (mkosi) and the core extension into image/out  [VERSION=]
	make image-variant     a second version copied from a built one, for image-check's fall back  [FROM= VERSION=]
	make image-check       boot the built image in a VM: pool, a stack, install onto a second disk
	make gen               regenerate sqlc, the daemon's OpenAPI document and the dashboard's schema
	make gen-check         gen, then fail if generated code differs from what is committed
	EOF

# Code generation: sqlc (internal/store), the OpenAPI document the daemon
# emits from its huma registrations (apps/core/api/openapi.json), and
# openapi-typescript (the dashboard's schema) from that document.
gen:
	(cd apps/core && go generate ./internal/store && go run ./cmd/daemon openapi > api/openapi.json)
	cd apps/dashboard && npx openapi-typescript ../core/api/openapi.json -o src/lib/api/schema.gen.ts

# Fails if committed generated code is out of sync with its sources.
GEN_PATHS = apps/core/internal/store apps/core/api/openapi.json apps/dashboard/src/lib/api/schema.gen.ts

gen-check: gen
	git diff --exit-code -- $(GEN_PATHS)
	untracked=$$(git ls-files --others --exclude-standard -- $(GEN_PATHS))
	if [ -n "$$untracked" ]; then echo "untracked generated files:"; echo "$$untracked"; exit 1; fi

# Two settings keep this fast. A race-built binary sleeps a second at exit by
# default, and tests that re-run the test binary as a helper pay it on every
# exit. `nix develop` makes a new TMPDIR on every entry, and Go's test cache
# keys on the TMPDIR a test reads, so nothing was ever cached. The path stays
# short: tests put unix sockets under it. One per checkout, so worktrees
# testing at the same time do not share it.
daemon-test: export GORACE := atexit_sleep_ms=0
daemon-test: export TMPDIR := /tmp/homelab-test-$(shell id -u)-$(shell pwd | cksum | cut -d' ' -f1)
daemon-test:
	mkdir -p "$$TMPDIR"
	cd apps/core
	unformatted=$$(gofmt -l .)
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet $(PKG)
	go test -race $(if $(RUN),-run '$(RUN)') $(PKG)
	go build ./cmd/daemon/ ./cmd/cli/

# The backup runner is its own module with no dependencies. Its fake rclone
# and restic are the race-built test binary, hence GORACE as above.
runner-test: export GORACE := atexit_sleep_ms=0
runner-test:
	cd apps/backup-runner
	unformatted=$$(gofmt -l .)
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go test -race $(if $(RUN),-run '$(RUN)') ./...
	CGO_ENABLED=0 go build -o /dev/null .

# FILE is relative to apps/dashboard. svelte-check fails on warnings too.
dashboard-test:
	cd apps/dashboard
	npm run check
	npx vitest run $(FILE)

dashboard-deps:
	cd apps/dashboard
	npm ci

nix-check:
	nix fmt -- --fail-on-change
	nix flake check --no-build

# Picks the checks from the files that differ from BASE (default HEAD: staged,
# unstaged and untracked). A file no rule knows runs everything. The nix checks share one
# eval cache, so the picked checks run in sequence.
verify-changed:
	want=""
	add() { for t in "$$@"; do case " $$want " in *" $$t "*) ;; *) want="$$want $$t" ;; esac; done; }
	while read -r f; do
		case "$$f" in
			"") ;;
			apps/backup-runner/*) add runner-test daemon-test ;;
			apps/core/api/*|apps/core/db/*|*.gen.go|*/schema.gen.ts) add gen-check daemon-test dashboard-test ;;
			apps/core/*.go) add gen-check daemon-test ;;
			apps/core/internal/config/*) add daemon-test ;;
			apps/core/*) add daemon-test ;;
			apps/dashboard/*) add dashboard-test ;;
			*.nix|flake.lock|nix/*) add nix-check ;;
			docs/*|*.md|scripts/run) ;;
			*) add verify ;;
		esac
	done < <({ git diff --name-only $(BASE); git ls-files --others --exclude-standard; } | sort -u)
	case " $$want " in *" verify "*) want="verify" ;; esac
	if [ -z "$$want" ]; then echo "Nothing to verify: only docs changed."; exit 0; fi
	echo "verify-changed:$$want"
	for t in $$want; do $(MAKE) --no-print-directory $$t; done
	echo "The checks for the changed files passed."

verify: gen-check
	@$(MAKE) --no-print-directory -j3 -O daemon-test runner-test dashboard-test nix-check
	echo "All repository verification checks passed."


# The image host (#95). Its tools (mkosi, QEMU) live in the image shell.
VERSION ?=
FROM ?=
KEEP ?=
BUILD ?=
CPUS ?=
MEM ?=
ON_HOST ?=
# The image targets refuse to run where homelab-daemon is running: a build
# there takes the memory the host's services need (2026-11-18, an installer
# build on the host ran it out of memory). Run them with scripts/builder, or
# set ON_HOST=1. A host kept for building marks itself with
# $(build_machine_marker) and is let through. The daemon's own update builds
# call image/build.sh directly.
build_machine_marker = /var/lib/homelab/build-machine
core_host_guard = @if [ -z "$(ON_HOST)" ] && [ ! -e $(build_machine_marker) ] && systemctl is-active --quiet homelab-daemon.service 2>/dev/null; then echo "make $@: this machine runs homelab core; run scripts/builder $@, mark a build machine with $(build_machine_marker), or set ON_HOST=1 to build here" >&2; exit 1; fi

image:
	$(core_host_guard)
	nix develop .#image -c image/build.sh $(VERSION)

image-variant:
	nix develop .#image -c image/variant.sh $(FROM) $(VERSION)

image-check:
	$(core_host_guard)
	nix develop .#image -c env KEEP=$(KEEP) BUILD=$(BUILD) CPUS=$(CPUS) MEM=$(MEM) image/check.sh

endif
