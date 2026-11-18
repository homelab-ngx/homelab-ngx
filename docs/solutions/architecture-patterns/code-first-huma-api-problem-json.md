---
title: Code-first API on Huma, with RFC 9457 errors and scoped operations
date: 2026-11-16
category: architecture-patterns
module: apps/core/internal/httpx
problem_type: architecture_pattern
component: api_layer
severity: high
applies_when:
  - Adding or changing a daemon endpoint
  - Returning an error from a handler
  - Deciding whether a stack may call an operation
  - Changing a PATCH body or a nullable field
tags: [openapi, huma, problem-json, rfc9457, codegen, scopes]
---

# Code-first API on Huma, with RFC 9457 errors and scoped operations

## Context

Until #82 the API was spec-first: a hand-written `openapi.yaml`, a 16,000-line oapi-codegen strict server and client (`internal/apigen`), and a redocly lint. Every handler converted between the generated types and the package's own, the spec had traps that failed silently (flow-style YAML, optional pointers configured in a side file), and the generated client was bigger than the CLI that used it. #82 also needed one operation registered on two listeners, the operator's socket and the stacks' listener, with a scope on each, which the spec could not carry.

## Guidance

- The Go handlers are the source of truth. Each feature package's `http.go` registers its operations with `httpx.Register(apis, scope, huma.Operation{…}, handler)`. A handler is `func(ctx, *In) (*Out, error)`; `In` carries `path`/`query` tags and a `Body`, `Out` a `Body`. `cmd/daemon/server.go` lists the features and builds both APIs (Huma v2 over the std mux, mounted at `/api`).
- The scope is one of `httpx.ScopeOperator` (unix socket only), `ScopeRead` (must be a GET; `Register` panics otherwise) or `ScopeControl` (start, stop and restart of stack units and stacks). Read and control operations are registered on the stack API too; operator operations do not exist there, so a stack gets 404 for them. New operations are operator unless a stack really needs them. The stack listener's checks are in [stack-contract.md](stack-contract.md).
- A handler that must refuse a stack something the operator may do checks `httpx.CallerStack(ctx)`: `services` answers 403 `core_unit` when a stack starts, stops or restarts a core or unknown unit.
- Go type names are the schema names, in one flat namespace. Two packages with a type of the same name panic at registration (`duplicate name`), so wire types get specific names (`BackupRun`, `StackGrant`, `SystemUpdateStatus`). They live in each package's `types.go`, and the CLI and tests use the same types.
- `huma.DefaultArrayNullable = false` (set in `httpx`): the daemon always sends `[]` for an empty list, and a nullable array would make every list in the dashboard's types `T[] | null`. Return an empty slice, never nil.
- A nullable object field is a pointer plus `TransformSchema`: Huma refuses the `nullable` tag on an object, so the type rewrites the property to `oneOf: [{$ref}, {type: null}]` (see `sysupdates.SystemUpdateStatus.Upgrade`). A nullable scalar is a pointer (`NextRun *time.Time`).
- A PATCH whose absent members stay as they are uses pointer fields with `omitempty` (`backups.BackupPatch`): absent leaves the field, a value sets it, and a list replaces the whole list when present. No operation needs to tell `null` from absent today.
- Errors are `*httpx.Problem`, which implements `huma.StatusError`. Return `httpx.NotFound("unknown_unit", …)` and friends; never write an error body. The body is RFC 9457 with `code` (what clients branch on) and extension members at the top level; `instance` is the request path. `huma.NewError` is replaced so Huma's own validation failures become `bad_request` (or `request_too_large`), and a plain error from a handler becomes a logged 500 `internal` whose text is not sent. A cancelled request is 499 `client_closed`, logged at debug. Unknown `/api/` paths get a problem 404.
- POST answers 200 by default (`Register` sets it); set `DefaultStatus: 204` for no body. Bulk operations return 200 with a `BulkResult` (`total`, `failed[{unit,error}]`), and the CLI exits non-zero when `failed` is not empty.
- `make gen` runs sqlc, writes `apps/core/api/openapi.json` from `go run ./cmd/daemon openapi`, and runs `openapi-typescript` on it into `apps/dashboard/src/lib/api/schema.gen.ts`. Commit both with the change; `make gen-check` fails on drift. The document's version is fixed at `1` so it does not change every commit. `homelab-daemon openapi --stack` prints the stack listener's document, and both listeners serve their own at `/api/openapi.json`; stack repos write their clients against that.
- The CLI has no generated client. `cmd/cli/client.go` has `call[O](ctx, method, path, in)` and `send(…)` over the unix socket, decoding into the handlers' own output types and turning a problem body into an `*APIError`. Escape path segments from user input with `url.PathEscape`.
- sqlc is the only Go code generation left. There is one `/api` prefix and no version segment, and no streaming operation.

## Why This Matters

The dashboard cannot drift from the server silently: its types come from the document the daemon emits, and a stale client fails type checking. The CLI cannot drift either, because it compiles against the handlers' types. The daemon, CLI and dashboard ship from one flake in one deploy, so no consumer needs a stable version; the stacks that call the API (`homelab-ha`) are written against the stack document and break only when an operation they use changes.

The scope sits on the operation itself, so the stack listener can never offer more than was registered for it. `httpx` tests that the stack API serves only scoped operations, and `stacks` walks its operations with each kind of token (`TestStackAPIAuth`).

Rejected alternatives: keeping the YAML spec with oapi-codegen (two sources of truth in practice, 16k generated lines, and no way to put one operation on two listeners with a scope), generating a Go client from the document (the handlers' types already exist), a `{success, data, error}` envelope (it duplicated the HTTP status and caused five error shapes before 2026-10), gRPC or Connect (browsers need a proxy), and a version prefix (nothing outside the flake consumes the operator API).

## Related

- [daemon-feature-packages.md](daemon-feature-packages.md)
- [stack-contract.md](stack-contract.md)
- [homelab-cli-automation.md](../conventions/homelab-cli-automation.md)
