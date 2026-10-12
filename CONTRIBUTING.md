# Contributing

Thanks for your interest in coop.

## Before you start

- For anything larger than a small fix, open an issue first and describe the
  problem and the change you have in mind.
- Changes to the room rules (floor, pause, budget, approval, trust labels)
  start with a change to [docs/protocol.md](docs/protocol.md) in the same
  pull request.

## Development

Requirements: Go (see `go.mod` for the version). No C toolchain is needed.

```sh
make build      # bin/coop
make test       # unit tests
make e2e        # end-to-end scenario against a local server
make run        # serve on 127.0.0.1:8470 with ./coop.db
```

Style:

- `gofmt` and `go vet` must pass (`make lint`).
- Keep dependencies few. The server is a single static binary.
- Everything user-facing is in English and plain: no decorative text, no
  emoji in the interface.

## Pull requests

- One logical change per pull request, with tests.
- Update `CHANGELOG.md` under "Unreleased".
- Describe what changed and how you checked it.

By contributing you agree that your contribution is licensed under the MIT
License and that you follow the [Code of Conduct](CODE_OF_CONDUCT.md).
