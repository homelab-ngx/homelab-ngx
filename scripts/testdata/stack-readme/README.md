# stack-example

What the stack is, written by hand.

<!-- manifest:start -->
<!-- Made from homelab-stack.json. Change the manifest, not these lines. -->

## Grants

You approve each of these before the stack starts.

- Route: `photos.<your domain>` to port 2283 in the stack, with requests up to 50GB.
- Route: `photos.example.org` to port 2283 in the stack.
- Storage: the folder in `DATA_DIR`, at `/data` in the stack, read-write.
- Storage: the folder in `MEDIA_DIR`, at `/media` in the stack, read-only.
- GPU: the render devices of the host, `/dev/dri`.
- Network: out to the host on port 5432/tcp.
- Network: out to the addresses you give in `CAMERAS` on port 554/tcp.
- Port: 32400/tcp on the host to port 32400 in the stack, from the LAN of the host, the addresses you give in `CLIENTS`, `10.0.0.0/8`.
- Port: 8555/udp on the host to port 8555 in the stack, from the LAN of the host.
- Port: 8554/tcp on the host to port 8554 in the stack, from the LAN of the host.
- Containers: Podman runs in the stack.
- API: the stack reads the state of the host from the daemon.
- API: the stack starts, stops and restarts stacks and their units.

## Variables

| Name | Kind | Required | Default | Description |
| --- | --- | --- | --- | --- |
| `DATA_DIR` | path | yes |  | The library. |
| `MEDIA_DIR` | path | no | `/pool/media` | Films to read. |
| `LOG_LEVEL` | env, string: `info`, `debug` | no | `info` | How much the server logs. |
| `API_KEY` | secret | yes |  | The key for a\|b lookups. |
| `CAMERAS` | address | no |  | The cameras. |
| `CLIENTS` | address | no |  | The players. |

## Backup

The stack recommends these folders. None is backed up until you add it to a backup.

- Library: the folder in `DATA_DIR`.
  - Without `/cache/**`. The server makes it again.
- Database dump: `/var/backup/postgresql` in the stack.

A backup run starts `dump.service` in the stack and waits for it before it copies.

## Health

An update is kept when all of this holds, and rolled back when it does not.

- `app.service` is active.
- `postgresql.service` is active.
- Port 2283 answers at `/ping`.

Manifest version 8, built with nixpkgs `nixos-25.11`. Runs on [homelab-ngx](https://github.com/homelab-ngx/homelab-ngx).

<!-- manifest:end -->

## License

MIT.
