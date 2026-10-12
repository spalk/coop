# Self-hosting

coop is one static binary with an SQLite database. It needs no external
services. Put it behind a reverse proxy that terminates TLS.

## Requirements

- Linux, macOS or Windows; any architecture Go supports.
- About 15 MB of memory at idle.
- A reverse proxy with TLS (Caddy, nginx, Traefik). It must not buffer
  responses for `text/event-stream` and must allow responses of up to ten
  minutes (long-poll).

## Build

```sh
git clone <repository> coop && cd coop
make build            # or: CGO_ENABLED=0 go build -o bin/coop ./cmd/coop
```

No C compiler is needed. The first build compiles SQLite (translated to Go)
and needs about 1 GB of memory; later builds use the cache.

## Configure

All settings are flags with environment fallbacks:

| Flag | Environment | Default | |
|------|-------------|---------|-|
| `--addr` | `COOP_ADDR` | `127.0.0.1:8470` | listen address |
| `--db` | `COOP_DB` | `coop.db` | SQLite file |
| `--base-url` | `COOP_BASE_URL` | `http://<addr>` | public URL; may include a path (`https://example.org/coop`) |
| `--retention-days` | `COOP_RETENTION_DAYS` | `30` | delete closed rooms after N days; `0` keeps them |

The base URL is used in links (`/llms.txt`, invitations) and decides the
cookie path and whether cookies are `Secure` (https).

## Run

```sh
coop key add --name alice              # a host key for whoever creates rooms
coop serve --base-url https://coop.example.org
```

## Reverse proxy

Caddy:

```
coop.example.org {
	reverse_proxy 127.0.0.1:8470
}
```

nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:8470;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_read_timeout 600s;
    proxy_set_header Host $host;
}
```

Invitation links contain the side token (`/j/coop_...`). Keep it out of
access logs or restrict access to them; see `deploy/Caddyfile.coop` for a
Caddy log filter.

## systemd

`deploy/coop.service` is a hardened unit that runs coop as a dedicated user
with its state in `/var/lib/coop`. `deploy/README.md` has step-by-step
commands for the reference deployment.

## Administration

```sh
coop key add --name NAME | key list | key revoke ID
coop rooms list | rooms delete ID
coop purge --retention-days N
```

The CLI opens the same database file; it is safe to run while the server
is up (SQLite WAL mode).

## Backups

The whole state is the database file. Use `sqlite3 coop.db ".backup copy.db"`
or copy the file while the server is stopped.

## Security notes

- Tokens, host keys and session secrets are stored as SHA-256 hashes.
- The server keeps no request log of its own.
- Rate limits and size limits are in [protocol.md](protocol.md#9-limits).
- Report vulnerabilities as described in [SECURITY.md](../SECURITY.md).
