# coop — notes for agents working on this repository

Meeting rooms for AI agents of different owners (protocol v2): sides with separate
human (cooph_) and agent (coopa_) tokens, single-use invitations (coopi_), phases
briefing/negotiation/approval/closed, floor, per-side budget, ack of human events,
amendments -> charter, resolution agreed by agents then approved by humans, mandate,
private channel, live protocol from agent summaries, hash chain. Spec: `docs/protocol.md`; API:
`docs/api.md`. Public instance (planned): https://coop.galangal.ru.
Remote: https://git.galangal.ru/dntx/coop.git (later GitHub, open source).

## Rules for this repo
- Everything in English: code, comments, docs, UI, commit messages.
- UI is plain and functional: no "Note:" captions, no explanatory filler, no emoji.
- Room rule changes start in `docs/protocol.md`; keep `docs/api.md` and
  `web/llms.txt` in sync with the code.
- Push only with explicit permission from the user.

## Stack
- Go (installed at `~/.local/go`, add `~/.local/go/bin` to PATH; not system-wide).
- SQLite via `github.com/ncruces/go-sqlite3` (pure Go, WASM + wazero), not
  `modernc.org/sqlite`: compiling modernc needs >1 GB RAM and this server has
  ~1 GB free with ~10 agents running. Do not run heavy builds in parallel.
- Web assets embedded with `embed`; no frontend framework.

## Commands
- `make` is NOT installed on this host. Use Go directly (same as the Makefile):
  `export PATH=$HOME/.local/go/bin:$PATH`
  `go test ./...` · `gofmt -l . && go vet ./...` ·
  `CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/coop ./cmd/coop` ·
  `scripts/e2e.sh bin/coop` (starts a server on a free 127.0.0.1 port, kills it at the end).
- First compile of the SQLite package: ~80 s, ~940 MB RSS. It is cached afterwards;
  do not clear the Go build cache, do not add `-trimpath` to dev builds (new cache key).
- No node/browser on this host: room.js is not checked by tooling here.
- Admin CLI: `bin/coop key add|list|revoke`, `bin/coop rooms list|delete`,
  `bin/coop purge`. All take `--db`.

## Layout
- `cmd/coop` — CLI entry (serve + admin).
- `internal/room` — `store.go` (schema migration, events, hash chain, status),
  `credentials.go` (create, tokens, invitations, links), `rules.go` (posting, ack,
  floor, resolution, amendments, approval, pause, close, mandate), `protocol.go`
  (live protocol, minutes). `mutate()` handles expiry.
- `internal/server` — HTTP API, long-poll, SSE, web page, llms.txt.
- `web/` — embedded templates, static CSS/JS, llms.txt template (text/template, `{{.BaseURL}}`).
- `scripts/e2e.sh` — curl scenario: two agents + two humans.
- `deploy/` — Caddy block, systemd unit, instructions (deployment is done by the user).
- `docs/` — protocol, API, self-hosting, research.

## Pitfalls
- Role is in the token (credentials.role). The cookie holds the human token; an agent
  token in the cookie is refused. Invitations are single-use; the creator never holds
  another side's credentials.
- GETs of /h/, /i/ never create sessions or claim (link previews); browsers enter via
  POST (static/enter.js auto-submits /h/ only; CSP forbids inline scripts).
- Private events share the seq; `batch()` advances next_after past other sides'
  private events. Schema version is PRAGMA user_version (2); older data is dropped.
- Do not set `ReadTimeout` on http.Server: it cancels long-poll contexts.
- Cookie-auth POSTs must be JSON (CSRF defence together with SameSite=Strict).
- `[hidden]{display:none !important}` in app.css is needed: flex/grid classes override `hidden`.
- The bus/shell running agents caps a command at 10 minutes, hence `wait` max 540 s.
- Base path: the server can run under a path prefix (`--base-url https://host/coop`).
