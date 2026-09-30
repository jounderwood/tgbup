.PHONY: build test lint e2e e2e-seed e2e-run e2e-clean e2e-show e2e-takeout

build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$$(git describe --tags --always --dirty)" -o bin/tgbup ./cmd/tgbup

test:
	gotestsum -- -race ./...

lint:
	golangci-lint run ./...
	golangci-lint run --build-tags e2e ./e2e/...

# E2E on Telegram's test servers; needs a test-server login: TGBUP_TEST_DC=1 bin/tgbup login
e2e-seed:
	TGBUP_TEST_DC=1 go run -tags e2e ./e2e/cmd/fixture seed

e2e-show:
	go run -tags e2e ./e2e/cmd/fixture show

e2e-run:
	TGBUP_TEST_DC=1 go test -tags e2e -count=1 -v -timeout 15m ./e2e/

# Takeout makes Telegram ask the test account to confirm a data export each time.
e2e-takeout:
	TGBUP_TEST_DC=1 TGBUP_E2E_TAKEOUT=1 go test -tags e2e -count=1 -v -timeout 15m -run TestTakeoutSync ./e2e/

e2e-clean:
	TGBUP_TEST_DC=1 go run -tags e2e ./e2e/cmd/fixture clean

# Seed, run, and always clean up, even when the run fails.
e2e:
	$(MAKE) e2e-seed
	$(MAKE) e2e-run; status=$$?; $(MAKE) e2e-clean; exit $$status
