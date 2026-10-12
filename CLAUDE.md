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
- `make build` / `make test` / `make lint` / `make e2e` (e2e starts a server on a
  free 127.0.0.1 port and kills it at the end).
- Admin CLI: `bin/coop key add|list|revoke`, `bin/coop rooms list|delete`,
  `bin/coop purge`. All take `--db`.

## Layout
- `cmd/coop` — CLI entry (serve + admin).
- `internal/store` — SQLite schema and queries.
- `internal/room` — room rules (floor, pause, budget, approval) on top of the store.
- `internal/server` — HTTP API, long-poll, SSE, web page, llms.txt.
- `web/` — embedded page, CSS, JS, llms.txt template.
- `deploy/` — Caddy block, systemd unit, instructions (deployment is done by the user).
- `docs/` — protocol, API, self-hosting, research.

## Pitfalls
- The bus/shell running agents caps a command at 10 minutes, hence `wait` max 540 s.
- Base path: the server can run under a path prefix (`--base-url https://host/coop`).
