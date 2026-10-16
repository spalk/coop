# coop — notes for agents working on this repository

Meeting rooms for AI agents of different owners (protocol v2): sides with separate
human (cooph_) and agent (coopa_) tokens, single-use invitations (coopi_), phases
briefing/negotiation/approval/decided/closed, floor, per-side budget, ack of human
events, amendments -> charter, resolution agreed by agents then approved by humans,
mandate, private channel, live protocol from agent summaries, hash chain.
Spec: `docs/protocol.md`; API: `docs/api.md`.

## Rules for this repo
- Everything in English: code, comments, docs, UI, commit messages.
- UI is plain and functional: no "Note:" captions, no explanatory filler, no emoji.
- Room rule changes start in `docs/protocol.md`; keep `docs/api.md` and
  `web/llms.txt` in sync with the code.
- Never commit secrets, tokens, real room logs or anyone's personal data.

## Stack
- Go (version in `go.mod`), no cgo.
- SQLite via `github.com/ncruces/go-sqlite3` (pure Go, wasm2go), not
  `modernc.org/sqlite`: compiling modernc needs more than 1 GB of RAM.
- Web assets embedded with `embed`; no frontend framework.

## Commands
- `make lint` (gofmt, go vet) · `make test` · `make build` · `make e2e`.
  Without make: `go test ./...` · `gofmt -l . && go vet ./...` ·
  `CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/coop ./cmd/coop` ·
  `scripts/e2e.sh bin/coop` (starts a server on a free 127.0.0.1 port, kills it at the end).
- First compile of the SQLite package takes about 80 s and about 1 GB of RAM; it is
  cached afterwards. Avoid `-trimpath` in dev builds (new cache key).
- `web/static/room.js` has no automated tests; check the room page in a browser.
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
- `deploy/` — Caddy block, systemd unit, instructions.
- `docs/` — protocol, API, self-hosting, research.

## Pitfalls
- Role is in the token (credentials.role). The cookie holds the human token; an agent
  token in the cookie is refused. Invitations are single-use; the creator never holds
  another side's credentials.
- GETs of /h/, /i/ never create sessions or claim (link previews); browsers enter via
  POST (static/enter.js auto-submits /h/ only; CSP forbids inline scripts).
- Private events share the seq; `batch()` advances next_after past other sides'
  private events. Schema version is PRAGMA user_version (`schemaVersion` in store.go).
- Do not set `ReadTimeout` on http.Server: it cancels long-poll contexts.
- Cookie-auth POSTs must be JSON (CSRF defence together with SameSite=Strict).
- `[hidden]{display:none !important}` in app.css is needed: flex/grid classes override `hidden`.
- Many agent shells cap a command at 10 minutes, hence `wait` max 540 s.
- Base path: the server can run under a path prefix (`--base-url https://host/coop`).
