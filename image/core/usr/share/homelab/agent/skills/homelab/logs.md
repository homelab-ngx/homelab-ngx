# Logs on homelab-ngx

A stack's services log to the stack's own journal, not the host's.
`journalctl -u <unit>` on the host finds nothing for them. The same goes for
`homelab services logs`, which reads only the host's units.

## Where each log is

| What | Command |
|---|---|
| A service in a running stack | `journalctl -M <stack> -u <unit> -n 200 --no-pager` |
| A whole running stack | `journalctl -M <stack> -p warning -n 200 --no-pager` |
| A stopped stack | `journalctl --directory=/var/lib/homelab/appdata/stacks/<stack>/root/var/log/journal -u <unit> -n 200 --no-pager` |
| The container (boot, shutdown, nspawn errors) | `journalctl -u homelab-stack-<stack> -n 100 --no-pager` |
| The daemon | `journalctl -u homelab-daemon -n 200 --no-pager` |
| Caddy (certificates, routes, upstream errors) | `journalctl -u caddy -n 200 --no-pager` |
| A backup | `homelab backup runs <backup>`, then `homelab backup run-show <run>`; `homelab backup logs <backup>` |
| Any failed host unit | `systemctl --failed`, then `journalctl -u <unit>` |

`<unit>` inside a stack is the name without the `<stack>-` prefix that
`homelab services status` adds. A unit whose name already starts with the
stack's name keeps it.

The daemon writes one JSON object per line with a syslog priority, so
`-p warning` and `-p err` filter it. Add `--since "1 hour ago"` to any
`journalctl` to narrow it, and `-o cat` to drop the timestamps.

## Install, update and rollback attempts

The daemon stores each attempt with its log, also after the stack was
rolled back:

```sh
curl -s --unix-socket /run/homelab-daemon/daemon.sock \
  'http://unix/api/stack-updates?stack=<stack>&limit=5' | jq
curl -s --unix-socket /run/homelab-daemon/daemon.sock \
  'http://unix/api/stack-updates/<id>' | jq -r .log
```

An attempt's `result` is `running`, `healthy`, `rolled_back`,
`build_failed`, `failed` or `interrupted`, and `reason` says why it did not
end healthy. `log` (only on a single attempt) holds the phases and the build
output.

`homelab stacks show <stack>` prints the last operation and its error,
but only since the daemon started.

## The daemon's log API

The dashboard's Logs page reads the same journals through the daemon:

```sh
curl -s --unix-socket /run/homelab-daemon/daemon.sock http://unix/api/log-viewer-config | jq
curl -s --unix-socket /run/homelab-daemon/daemon.sock \
  'http://unix/api/logs?unit=<id>&lines=200&priority=4&q=<text>' | jq
```

- `unit` is an id from `log-viewer-config`: a core unit (`homelab-daemon`),
  a backup's unit, or a stack's unit as `<stack>-<unit>`.
- `lines` is 50 by default and 500 at most. Lines come newest first.
- `priority` is the highest syslog priority to include: 3 error, 4 warning,
  6 info, 7 debug.
- `q` is text the message must contain, ignoring case.
- `before` and `after` take a journal cursor from a previous page.

The whole API is described at
`curl -s --unix-socket /run/homelab-daemon/daemon.sock http://unix/api/openapi.json`.

## Finding why something fails

1. `homelab doctor`: host problems first (disk full, the daemon or Caddy
   down, failed units).
2. `homelab --json stacks list` and `homelab stacks show <stack>`: is the
   stack running, and did its last operation fail?
3. `homelab services status`, or `systemctl -M <stack> list-units
   --failed`: which service is down.
4. That service's journal, from its first error, not its last line. A
   service that restarts repeats the same error; find the first one.
5. For a failed install or update, the attempt's log under
   `/api/stack-updates`. A build error is there, not in the stack's journal.
6. If a service answers but its page does not load, Caddy's journal for
   the hostname.

Quote the line that shows the cause. When the logs don't show it, say so
and say what to check next. Never repeat a password, key or token that
appears in a log.
