---
title: How the power meter reads a smart plug through Home Assistant
date: 2026-11-23
category: architecture-patterns
module: apps/core/internal/powermeter
problem_type: architecture_pattern
component: service_layer
severity: low
applies_when:
  - Changing how the daemon talks to Home Assistant
  - Debugging a power meter that shows no reading
  - Changing the power profile comparison
tags: [power, home-assistant, websocket, metrics, rapl]
---

# How the power meter reads a smart plug through Home Assistant

## Context

RAPL counts the CPU package only. On a Core i3-10105 it read 0.9 W at
PC10 and 2.8 W at PC3, while most of what a deeper package state saves is in
the chipset and the links, out of its sight. A smart plug sees the whole
machine. Many home servers already have one in Home Assistant, so the daemon
reads it from there rather than from the plug's own protocol.

## Guidance

- **One WebSocket subscription** (`internal/powermeter/ha.go`). The daemon
  connects to `<address>/api/websocket`, answers `auth_required` with the
  long-lived token, then sends `subscribe_entities` with the one entity id.
  The first event carries the entity's state and attributes under `a`;
  later events carry what changed under `c.<entity>.+`, often the state
  alone, so the unit and name are kept from earlier events. No other part
  of the API is used. Polling `GET /api/states/<entity>` would need a
  request every few seconds for the same result.
- **Units.** `W`, `kW` and `mW` are turned into watts. Any other unit is not
  power: Test answers `not_power`. A state that is not a number, such as
  `unavailable` while the plug is offline, is no reading.
- **A steady value is still a reading.** A plug reports when its value
  changes, so a host drawing the same 14.2 W for ten minutes sends nothing
  for ten minutes. The last value counts while the connection is up. A ping
  every 30 s finds a Home Assistant that went away without closing; a
  closed connection means no reading at once.
- **Reconnecting.** After a failure the daemon waits 5 s, doubling up to 5
  min, and starts again from 5 s after a connection that held a minute. An
  `auth_invalid` is not retried: the token stays refused until it is
  changed, and the state says `auth_failed`. Changing the address, entity or
  token reconnects at once; changing only `idle_only` does not.
- **The token** is sealed with the host's age key in the `secret` column of
  `host_settings` (section `power_meter`) and never returned; the API says
  `has_token`.
- **Into the metrics.** `metrics.SetWall` gives the sampler the reading, so
  every 2 s point has `power_wall` beside `power_cpu`. `metrics.SetProfile`
  gives it the power profile, stored with each minute of the host in
  `metrics.db`'s `profile` column. Neither package imports the other.
- **The comparison** (`GET /metrics/power-profiles`) averages the stored
  minutes per profile. With `idle_only` it keeps the minutes whose host CPU
  was under 5%, so a profile that happened to run during a backup does not
  look worse. Minutes from before the profile was recorded have none and
  are left out. `idle_only` is a display choice stored with the meter's
  settings; the minutes are the same either way. The history's
  `profile_changes` are the minutes whose profile differs from the stored
  minute before, which the dashboard draws as dashed lines.

## Limits

- One meter. A host on two plugs, or a UPS that reports its load, is not
  added up.
- The plug's own report rate bounds how live the reading is: many report
  every few seconds, some only on a change of a watt or more.
- A minute's profile is the one in force at its last sample, so a change
  mid-minute counts that whole minute to the new profile.
- The reading is the plug's, with its own error, often a few percent at
  low loads. Compare profiles on the same plug, not against another meter.
