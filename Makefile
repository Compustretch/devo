.PHONY: build test install

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -X github.com/Compustretch/devo/internal/cli.appVersion=$(VERSION)

build:
	mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/devo ./cmd/devo

test:
	go test ./...

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/devo
