.PHONY: build test lint

build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$$(git describe --tags --always --dirty)" -o bin/tgbup ./cmd/tgbup

test:
	gotestsum -- -race ./...

lint:
	golangci-lint run ./...
