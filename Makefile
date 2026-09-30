.PHONY: build test lint e2e

build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$$(git describe --tags --always --dirty)" -o bin/tgbup ./cmd/tgbup

test:
	gotestsum -- -race ./...

lint:
	golangci-lint run ./...
	golangci-lint run --build-tags e2e ./e2e/...

# Needs a test-server account: TGBUP_TEST_DC=1 bin/tgbup login
e2e:
	TGBUP_TEST_DC=1 go test -tags e2e -count=1 -v -timeout 15m ./e2e/
