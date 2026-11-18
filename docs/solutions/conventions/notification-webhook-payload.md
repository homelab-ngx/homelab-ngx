---
title: The notification webhook payload
date: 2026-11-18
category: conventions
module: apps/core/internal/notifications
problem_type: convention
component: notifications
symptoms:
  - A receiver needs to know what a webhook channel sends
root_cause: missing_documentation
resolution_type: documentation
severity: low
tags: [notifications, webhook, ntfy]
---

# The notification webhook payload

A webhook channel POSTs one JSON object per notification (`Payload` in
`internal/notifications/senders.go`). It is the only wire format core
speaks; a receiver that wants something else adapts on its side.

```json
{
  "title": "Disk almost full",
  "message": "Disk almost full · /pool 95%",
  "severity": "alert",
  "key": "audit:disk",
  "url": "https://homelab.example.com/storage",
  "sent_at": 1762876800
}
```

- `title`, `message`: one of them is non-empty. A resolved note has a title
  and an empty message.
- `severity`: `alert` (act on it) or `info`.
- `key` (omitted when none): the problem key. A receiver may replace an
  earlier notification with the same key, so the resolved note replaces the
  alert. A message about several problems has no key.
- `url` (omitted when the dashboard address is unknown): the page to open.
- `sent_at`: unix seconds.
- Headers: `Content-Type: application/json`, `User-Agent: homelab`, and
  `Authorization: Bearer <token>` when the channel has a token.
- A 2xx answer is delivered. Any other status fails the channel; the
  answer's `error` field, when there is one, is shown on the page.
- Redirects are not followed, so a token never goes to another address. A
  certificate pin replaces the chain check (self-signed receivers).

ntfy gets the same notification through ntfy's JSON publishing: priority 4
and a warning tag for an alert, 3 for info, the `url` as the click action,
and a title alone sent as the message.
