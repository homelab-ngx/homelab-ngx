---
title: The dashboard's sign-in sits on the web socket only
date: 2026-11-18
category: architecture-patterns
module: apps/core/internal/auth
problem_type: architecture_pattern
component: authentication
severity: high
applies_when:
  - Exposing the daemon's API through a new route or listener
  - Adding an operation a browser calls without a session
  - Changing the dashboard's Caddy snippet or the daemon's sockets
tags: [auth, sign-in, password, session, caddy, csrf, sockets]
---

# The dashboard's sign-in sits on the web socket only

## Context

The operator API used to have no login. Caddy proxied `/api/*` to the CLI's unix socket, whose file mode was the only check, so anyone who reached Caddy had the whole API. A stack could reach the host's port 443 too, send the dashboard's Host header and approve its own grants.

## Guidance

- **One password, no users.** The first visitor to a fresh host sets it (`POST /auth/setup`, refused with `409 password_set` after). Until then every other call answers `401 setup_required`, so nothing is usable before a password exists.
- **Two sockets, one API.** The daemon serves the same operator API twice:
  - `/run/homelab-daemon/daemon.sock` (group `homelab-daemon`) for the CLI, with no sign-in. Opening it already takes root or that group, and root can set or remove the password (`homelab auth set-password`, `homelab auth reset`).
  - `/run/homelab-daemon/web.sock` (group `caddy`) behind `auth.Gate`. Caddy is not in `homelab-daemon`, so the web socket is its only way in.
- **The gate**, in order:
  1. A request from another origin is refused (`403 cross_origin`). Over HTTPS the browser's `Sec-Fetch-Site` must say `same-origin`. Without it, a GET passes and anything else (a WebSocket upgrade included) needs an `Origin` naming this host. A stack's site under the same domain is same-site but not same-origin, so it is refused.
  2. `GET /auth` and `POST /auth/{setup,login,logout}` pass.
  3. Everything else needs a live session: `401 setup_required` or `401 login_required`. The dashboard sends the page back to its sign-in screen on either code.
- **Sessions** are 32 random bytes in an `HttpOnly`, `SameSite=Strict` cookie. Over HTTPS it is `__Host-homelab_session` with `Secure`, which a sibling host cannot overwrite. Over plain HTTP, before the host has a domain, it is `homelab_session`. The database keeps only the token's SHA-256. A session ends 30 days after its last use, and changing the password ends every session.
- **The password** is stored as argon2id (64 MiB, two passes), at most two hashes run at once, and a client gets five wrong passwords per 15 minutes (the address is the last `X-Forwarded-For` entry, which Caddy writes).
- **Caddy** (`image/core/usr/share/homelab/caddy/dashboard.caddy`):
  - refuses `10.99.0.0/16` before anything else, inside a `route` so directive sorting cannot move the refusal after a `handle`;
  - on `:80`, once the host has a domain, imports the daemon's `redirect.caddy`, which sends the browser to the HTTPS name, so the password crosses the network in the clear only before a domain is set.
- **The installer stick** has no database and no password: it serves its small API ungated on both sockets, and the dashboard treats a `404` from `/auth` as "no sign-in here".

## Why This Matters

The operator API approves grants, installs stacks and changes the firewall. One gate in front of every route, deny by default, keeps a new operation from being exposed by accident: an operation is open only when it is added to the gate's list.

## When to Apply

- A new way to reach the daemon from the network serves the gated handler, never the CLI's.
- An operation a browser must call while signed out goes in `open` in `internal/auth/gate.go`, with a test.
- A cookie-authenticated endpoint that is not a fetch (a WebSocket, an `EventSource`) still passes the origin check; keep it a GET or send an `Origin`.

## Related

- `stack-contract.md`: what a stack may reach on the host.
- `homelab-cli-automation.md`: the CLI's socket.
