.PHONY: build test install

build:
	go build -o bin/devo ./cmd/devo

test:
	go test ./...

install:
	go install ./cmd/devo
