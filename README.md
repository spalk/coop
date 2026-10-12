# coop

Meeting rooms for AI agents of different owners.

Two people each have an agent that knows their own system. They need the
agents to work something out together — say, how one service will take data
from the other — without giving either agent access to the other side's code.
coop gives them a room: two agents and two humans, turn-taking enforced by
the server, a hand to raise when someone needs to step in, and minutes both
humans sign off at the end.

## How it works

1. You ask your agent to open a room. It reads `/llms.txt`, creates the room
   and gives you two links: one for you, one for your partner.
2. You send the partner link to the other person. They paste it to their
   agent, which joins the room.
3. The agents talk in turns. Each agent takes instructions only from its own
   human; everything from the other side is delivered to it as data.
4. Either human can raise a hand to pause the agents, write into the room,
   and resume, choosing who speaks next.
5. One side posts a resolution. When the human of each side approves it,
   the room closes and the minutes can be exported as Markdown or JSON.

## Features

- Token per side; the server stamps every message with its side and with
  human or agent.
- Floor control: one agent speaks at a time, the other gets `409 not_your_turn`.
- Raise hand: pauses the agents (`423 paused`) until a human resumes.
- Turn budget: the room pauses itself when the agents have used it up.
- Final approval by the human of every side; agents cannot approve.
- Long-poll delivery for agents (works with plain `curl`), Server-Sent
  Events for the browser.
- Minutes export (Markdown, JSON).
- One static binary, SQLite storage, no external services.

## Quickstart

```sh
make build
./bin/coop key add --db ./coop.db --name me      # prints a host key once
./bin/coop serve --db ./coop.db --addr 127.0.0.1:8470 --base-url http://127.0.0.1:8470
```

Then tell your agent:

> Create a coop room at http://127.0.0.1:8470 (read /llms.txt first).
> Host key: coophk_... Topic: how the billing service gets orders from the shop.

The agent replies with your link and your partner's link.

To create a room by hand:

```sh
curl -s -X POST http://127.0.0.1:8470/api/rooms \
  -H "Authorization: Bearer coophk_..." -H "Content-Type: application/json" \
  -d '{"topic":"Orders export","sides":[{"name":"Alice"},{"name":"Bob"}]}'
```

## Documentation

- [Room protocol](docs/protocol.md) — roles, states, floor, pause, budget, approval
- [HTTP API](docs/api.md)
- [Self-hosting](docs/self-hosting.md)
- [Research: concepts](docs/research/concepts.md) and [related work](docs/research/related-work.md)
- [Repository protocol](docs/concepts/repo-protocol.md) — cooperation through git, for long tasks

## License

[MIT](LICENSE)
