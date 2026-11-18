---
title: Daemon feature packages wired in one composition root
date: 2026-10-15
category: architecture-patterns
module: apps/core
problem_type: architecture_pattern
component: service_layer
severity: medium
applies_when:
  - Adding a daemon feature, endpoint, or long-running loop
  - Deciding where shared daemon code belongs
tags: [daemon, go, composition-root, http-adapter, cmdrunner, huma]
---

# Daemon feature packages wired in one composition root

## Context

`cmd/daemon` used to be one flat `package main`. `buildAPIMux` took 12 positional parameters, and about 1,300 lines of handler closures captured them. Business logic lived inside handlers, so nothing could be tested without HTTP. Handlers also mutated `*Config` while the monitor and scheduler read it, which was a data race.

## Guidance

- Each feature is a package under `apps/core/internal/` with a `New(...)` constructor, an `HTTP` adapter type in `http.go` and its wire types in `types.go`. Handlers hold no logic, and domain code never imports `net/http`.
- Constructors take their dependencies as explicit arguments, such as `storage.New(runner, systemd, cfg, log)`. A package declares the small interfaces it needs. For example, `config.OverrideStore` is implemented by an adapter in `configstore`, so `config` never imports `store`.
- `cmd/daemon/main.go` is the only file that knows every package. It builds them and starts the loops in one linear sequence; read that file for the boot order.
- `cmd/daemon/server.go` lists every `*HTTP` adapter in `features()`. Each adapter's `Register(httpx.APIs)` adds its operations with `httpx.Register` at their scope, on the operator API and, for read and control, the stack API ([code-first-huma-api-problem-json.md](code-first-huma-api-problem-json.md)). Two operations on the same route, or two wire types with the same name, panic at startup.
- Long-running loops live with their domain: the churn watch is in `services`, and the cron scheduler is in `backups`.
- Config is one immutable snapshot behind an `atomic.Pointer` (`config.Manager`). One writer path builds and swaps it, and readers never lock.
- Every exec goes through `cmdrunner`. Interactive TTY commands in the CLI are the one exception.
- Logging goes through `logging.Logger(module)`; calling `slog.Default()` directly is forbidden.
- There is one Go module, with no `replace` directives.
- Core keeps only what core needs (#82). A feature the host does not need runs in a stack repo and, if it needs the daemon, calls it through an `api` grant: a Home Assistant bridge, for example, is a stack with read and control scopes.

## Why This Matters

Changing a feature means reading one package and its `http.go`, and tests build the package with fakes for its interfaces. The cost is that `main.go` grows with every feature. That was accepted over hiding the wiring.

Rejected alternatives:

- An `App` or `Deps` struct passed everywhere hides what each package needs and lets every package reach everything.
- A DI framework (wire, fx) adds magic for about 15 constructors. The linear boot order already documents itself.
- A layered `handlers/` `services/` `repos/` split spreads one feature across three directories.
- Separate Go modules per feature bring back `replace` directives and stale `vendorHash` failures.

## When to Apply

- A new feature needs a package, a constructor, its `*HTTP` in `features()` in `server.go`, and one line in `main.go`.
- Shared plumbing goes in a leaf package (`httpx`, `cmdrunner`, `systemd`, `mounts`), because feature packages cannot import each other freely.

## Related

- [code-first-huma-api-problem-json.md](code-first-huma-api-problem-json.md)
