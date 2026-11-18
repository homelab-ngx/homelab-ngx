# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

**Today:** one operator who builds and runs a single NixOS home server. They use the dashboard at their desk on the LAN in deliberate sessions: adding a stack, reviewing and applying updates, reading logs, checking storage and backups. Claude Code and the `homelab` CLI cover the rest.

**Intended:** other homelab owners. The core host and this dashboard are meant to be fit to release as open source, so the UI must work for people less technical than the operator, who do not know Nix, systemd-nspawn or the repo's vocabulary.

## Product Purpose

The dashboard is where the operator sees whether the home server is healthy and manages the services on it. Services run in **stacks** (a repo with a flake and a manifest, run as its own NixOS system in a container), so updating or breaking a service never touches the core host. A good visit answers "is anything wrong or waiting?" first; the controls to act on it are one step deeper.

## Positioning

Unraid-style runtime management on NixOS: a stack is added by giving a repo, the operator approves the access it asks for (grants), and every install, update and rollback is a recorded generation that can be returned to. Core stays small and rarely changes.

## Operating Context

- Served by Caddy on the host's private domain, reachable only on the LAN and the VPN. Not public.
- Alerts go to the notification channels set on the Notifications page (ntfy, webhook).
- Desktop browser is the main scene. Phone use exists (PWA) but is secondary.
- The daemon API (`/api`, problem+json errors) is the only data source; the CLI exposes the same operations.

## Capabilities and Constraints

- Pages: Stacks (overview, a stack's page, add a stack), Storage, Backups, Notifications, Settings (including the core host). Updates and Logs are being folded into Stacks (2026-11-16); core and backup logs move under Settings.
- Stack operations: add from a git repo or a local folder, approve grants, fill in variables (env, secret, path), start, stop, restart, update, update all, roll back to a generation, remove (data kept).
- Built with SvelteKit (static SPA), Svelte 5 runes, Tailwind v4, bits-ui, TanStack Query and a client generated from the daemon's OpenAPI spec.
- Terminology follows the repo's `CONTEXT.md` (stack, grant, generation, attempt, variable, core).

## Brand Commitments

- Name: **homelab-ngx** (written "Homelab-ngx" as a title). The CLI, daemon, files and paths keep `homelab`.
- Voice: terse and neutral, the way a well-made tool labels itself. A page shows labels and values; a sentence is kept for an error, an empty state, a blocker, or what a destructive action does. No "you", no reassurance, no explaining a concept on the page. Owners less technical than the operator are served by the help icon next to a label, never by prose on the page. The rules and their limits are in DESIGN.md under "Copy".

## Evidence on Hand

- Real data only comes from the live daemon. No screenshots, demos, testimonials or user counts exist. Do not invent any.

## Product Principles

1. Health first: problems and pending work are visible without a click, and quiet when there are none.
2. Nothing happens behind the operator's back: approvals, restarts and removals say what they will do before they do it, and what they keep.
3. Every change can be undone: generations and rollbacks stay one step away from where an update is applied.
4. Labels and values on screen, detail on demand: explanations sit in help tips, not in paragraphs, and a fact appears once per screen.
5. Core is distinct from stacks: the core host is never mistaken for a stack control.
