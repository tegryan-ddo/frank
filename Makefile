# Frank CLI Makefile

.PHONY: all build install clean test lint fmt docker help

# Variables
BINARY_NAME=frank
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"
GOFLAGS=-trimpath

# Default target
all: build

# Build the binary
build:
	@echo "Building $(BINARY_NAME)..."
	go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) .

# Build for all platforms
build-all: build-linux build-darwin build-windows

build-linux:
	@echo "Building for Linux..."
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-linux-arm64 .

build-darwin:
	@echo "Building for macOS..."
	GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-darwin-arm64 .

build-windows:
	@echo "Building for Windows..."
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME)-windows-amd64.exe .

# Install to GOPATH/bin
install: build
	@echo "Installing $(BINARY_NAME)..."
	go install $(GOFLAGS) $(LDFLAGS) .

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod download
	go mod tidy

# Run tests
test:
	@echo "Running tests..."
	go test -v -race ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Lint the code
lint:
	@echo "Linting..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

# Format the code
fmt:
	@echo "Formatting..."
	go fmt ./...
	@if command -v goimports >/dev/null 2>&1; then \
		goimports -w .; \
	fi

# Build the Docker image
docker:
	@echo "Building Docker image..."
	docker build -t frank-dev:latest -f build/Dockerfile build/

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(BINARY_NAME)
	rm -f $(BINARY_NAME)-*
	rm -f coverage.out coverage.html

# Show help
help:
	@echo "Frank CLI Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make build       Build the CLI binary"
	@echo "  make build-all   Build for all platforms"
	@echo "  make install     Install to GOPATH/bin"
	@echo "  make deps        Install dependencies"
	@echo "  make test        Run tests"
	@echo "  make lint        Run linter"
	@echo "  make fmt         Format code"
	@echo "  make docker      Build Docker image"
	@echo "  make clean       Clean build artifacts"
	@echo "  make help        Show this help"
