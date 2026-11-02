# claude-agent-sdk-go
# Run `just` to list recipes.

set dotenv-load := false

default:
    @just --list --unsorted

# Format code
fmt:
    gofmt -w .

# Fail if any file needs gofmt
fmt-check:
    @test -z "$(gofmt -l .)" || (echo "gofmt needed on:"; gofmt -l .; exit 1)

# go vet, also with the live-suite build tag
vet:
    go vet ./...
    go vet -tags claudecli ./...

# golangci-lint v2 from the tools/lint tool module
lint:
    go tool -modfile=tools/lint/go.mod golangci-lint run
    go tool -modfile=tools/lint/go.mod golangci-lint run --build-tags claudecli

# Run tests
test:
    go test -count=1 ./...

# Run tests with the race detector
test-race:
    go test -race -count=1 ./...

# Run each fuzz target briefly
fuzz-smoke:
    ./scripts/fuzz-smoke.sh

# Live suite against a real CLI (needs CLAUDE_CODE_OAUTH_TOKEN; costs a few cents)
live:
    CLAUDE_LIVE=1 go test -tags claudecli -run Live -count=1 -v ./claude/

# Tidy Go modules
tidy:
    go mod tidy
    cd tools/lint && go mod tidy

# Everything CI runs
ci: fmt-check vet lint test-race fuzz-smoke

# Quick local checks
check: fmt-check vet test
