GO      ?= go
BIN     := bin/coop
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

export CGO_ENABLED := 0

.PHONY: all build release test e2e lint run clean

all: lint test build

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/coop

# Reproducible paths; compiles everything again (needs ~1 GB RAM for SQLite).
release:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/coop

test:
	$(GO) test ./...

e2e: build
	./scripts/e2e.sh $(BIN)

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	$(GO) vet ./...

run: build
	$(BIN) serve --addr 127.0.0.1:8470 --db ./coop.db --base-url http://127.0.0.1:8470

clean:
	rm -rf bin dist
