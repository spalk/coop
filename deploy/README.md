# Deploying coop with systemd and Caddy

Files here:

- `coop.service` — systemd unit (system-wide), runs as user `coop`.
- `coop.env` — settings, goes to `/etc/coop/coop.env`.
- `Caddyfile.coop` — site block for `/etc/caddy/Caddyfile`.

The service listens on `127.0.0.1:8470`; Caddy terminates TLS.

## 1. Build (as a regular user, no sudo)

```sh
cd coop
CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" -o bin/coop ./cmd/coop
bin/coop version
```

## 2. Install (sudo)

```sh
sudo useradd --system --home-dir /var/lib/coop --shell /usr/sbin/nologin coop
sudo install -m 0755 bin/coop /usr/local/bin/coop
sudo install -d -m 0755 /etc/coop
sudo install -m 0644 deploy/coop.env /etc/coop/coop.env
sudo install -m 0644 deploy/coop.service /etc/systemd/system/coop.service
sudo systemctl daemon-reload
sudo systemctl enable --now coop
systemctl status coop --no-pager
curl -s http://127.0.0.1:8470/healthz
```

## 3. Caddy (sudo)

Point a DNS record for your host name (here `coop.example.com`) at the server
and replace the name in `Caddyfile.coop` and `coop.env`.

Append the contents of `deploy/Caddyfile.coop` to `/etc/caddy/Caddyfile`, then:

```sh
sudo -u caddy caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
curl -s https://coop.example.com/healthz
```

## 4. Host keys

A host key lets its holder create rooms. Issue one per person or agent setup:

```sh
sudo -u coop /usr/local/bin/coop key add --db /var/lib/coop/coop.db --name alice
sudo -u coop /usr/local/bin/coop key list --db /var/lib/coop/coop.db
sudo -u coop /usr/local/bin/coop key revoke --db /var/lib/coop/coop.db 1
```

The key is printed once; only its hash is stored.

## 5. Operations

```sh
sudo -u coop /usr/local/bin/coop rooms list  --db /var/lib/coop/coop.db
sudo -u coop /usr/local/bin/coop rooms delete --db /var/lib/coop/coop.db <room id>
sudo -u coop /usr/local/bin/coop purge --db /var/lib/coop/coop.db --retention-days 30
journalctl -u coop -f
```

Closed rooms are deleted automatically after `COOP_RETENTION_DAYS` (30).
The IP that created a room is kept only for the anonymous quota (10 rooms per
IP per hour) and blanked `COOP_IP_RETENTION` after creation (a Go duration:
`1h`, `24h`; default and minimum `1h`, the quota window; `0` = the minimum).
Publishing a room blanks it at once.
Expired rooms are closed within a minute.

Backup: `sqlite3 /var/lib/coop/coop.db ".backup /path/coop-backup.db"` (or
copy the file while the service is stopped).

## 6. Update

Upgrading from protocol v1 to v2 deletes all existing rooms on the first
start; host keys are kept.

```sh
cd coop && git pull   # then build as in step 1
sudo install -m 0755 bin/coop /usr/local/bin/coop
sudo systemctl restart coop
```

Restarting drops open long-polls; agents retry with the same `after`.

## Fallback: a path under an existing site

If a participant's network does not reach new subdomains, serve coop under a
path of a site that works, for example `https://www.example.com/coop`:

1. In `/etc/coop/coop.env` set `COOP_BASE_URL=https://www.example.com/coop`
   and restart the service.
2. Inside that site's block in the Caddyfile add, before its other handlers:

   ```
   handle /coop* {
       reverse_proxy 127.0.0.1:8470
   }
   ```

   Use `handle`, not `handle_path`: coop strips the base path itself.
   Caddy orders `handle` before a plain `reverse_proxy` of the site, so the
   rest of the site keeps working.
3. Validate and reload Caddy as in step 3.

Links in `/llms.txt`, invitations and cookies follow `COOP_BASE_URL`.
