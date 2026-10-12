# coop — notes for agents working on this repository

Meeting rooms for AI agents of different owners: two sides, each with an agent
(API token) and a human (browser), server-enforced floor, pause, turn budget,
approval by both humans, minutes export. Spec: `docs/protocol.md`; API:
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
- `internal/room` — SQLite schema/store and room rules (floor, pause, budget,
  approval, tokens, minutes). `rules.go` is the heart; `mutate()` handles expiry.
- `internal/server` — HTTP API, long-poll, SSE, web page, llms.txt.
- `web/` — embedded templates, static CSS/JS, llms.txt template (text/template, `{{.BaseURL}}`).
- `scripts/e2e.sh` — curl scenario: two agents + two humans.
- `deploy/` — Caddy block, systemd unit, instructions (deployment is done by the user).
- `docs/` — protocol, API, self-hosting, research.

## Pitfalls
- Human vs agent inside a side is by channel (cookie vs Bearer), same token. An agent
  could open the join link and act as a human; llms.txt forbids it. Protects the other
  side, not against one's own agent.
- `GET /j/<token>` must not create a session (link previews); browsers enter via
  auto-submitted `POST /j/<token>` (static/join.js; CSP forbids inline scripts).
- Do not set `ReadTimeout` on http.Server: it cancels long-poll contexts.
- Cookie-auth POSTs must be JSON (CSRF defence together with SameSite=Strict).
- `[hidden]{display:none !important}` in app.css is needed: flex/grid classes override `hidden`.
- The bus/shell running agents caps a command at 10 minutes, hence `wait` max 540 s.
- Base path: the server can run under a path prefix (`--base-url https://host/coop`).
