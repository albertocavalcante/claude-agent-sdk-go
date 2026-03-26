# Default recipe
default: check

# Format code
fmt:
    gofmt -w .

# Check formatting
fmt-check:
    @test -z "$(gofmt -l .)" || (echo "Files need formatting:" && gofmt -l . && exit 1)

# Run linter
lint:
    go vet ./...

# Run tests
test:
    go test -race -count=1 ./...

# Run tests with verbose output
test-v:
    go test -race -count=1 -v ./...

# Build all packages
build:
    go build ./...

# Run all checks (format, lint, test, build)
check: fmt-check lint test build
