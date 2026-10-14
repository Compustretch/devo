.PHONY: build test install

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X github.com/Compustretch/devo/internal/cli.appVersion=$(VERSION)
GOTESTSUM_VERSION ?= v1.13.0

build:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/devo ./cmd/devo

test:
	go run gotest.tools/gotestsum@$(GOTESTSUM_VERSION) --format=testname --no-color=false -- ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/devo
