---
title: Caddy ACME DNS-01 with Cloudflare fails on router DNS and token scope
date: 2026-10-15
category: integration-issues
module: image/core (Caddy)
problem_type: integration_issue
component: infrastructure
symptoms:
  - ACME DNS-01 challenges for the host's domain fail or never propagate
  - Cloudflare API returns error 9109
root_cause: config_error
resolution_type: config_change
severity: high
tags: [caddy, acme, dns-01, cloudflare, tls, dns-rebind]
---

# Caddy ACME DNS-01 with Cloudflare fails on router DNS and token scope

## Problem

Caddy gets certificates for the host's wildcard domain (`*.example.com`) through Cloudflare DNS-01. Three separate causes made issuance fail, each with an unclear error.

## Symptoms

- ACME DNS resolution fails, or the propagation check never succeeds.
- The Cloudflare API answers with error 9109.

## Solution

- The LAN router's DNS rebind protection blocks public resolvers from returning RFC1918 addresses for the domain. Turn rebind protection off in the router settings.
- The Cloudflare API token needs `Zone:Zone:Read` and `Zone:DNS:Edit` on the zone. With the wrong scope, Cloudflare returns the opaque error 9109.
- Add `resolvers 1.1.1.1 8.8.8.8` inside each `tls {}` block, so the ACME propagation check does not query the router's broken IPv6 link-local DNS.

## Prevention

- When ACME fails on this host, check these three causes before changing the Caddy config.

## Related Issues

- [nixos-module-rules.md](../conventions/nixos-module-rules.md)
