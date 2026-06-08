BINARY      := llmroute
PKG         := github.com/ssthil/llmroute
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
GO          ?= go

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into ./bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

.PHONY: install
install: ## Install the binary into GOBIN
	$(GO) install -trimpath -ldflags "$(LDFLAGS)" .

.PHONY: run
run: ## Run the proxy locally
	$(GO) run . proxy

.PHONY: test
test: ## Run unit tests with the race detector
	$(GO) test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests and open an HTML coverage report
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

.PHONY: lint
lint: ## Run go vet and gofmt checks (falls back to vet if golangci-lint absent)
	$(GO) vet ./...
	@gofmt -l . | grep -E '.' && { echo "gofmt needed on the files above"; exit 1; } || echo "gofmt clean"
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed; ran go vet + gofmt"

.PHONY: fmt
fmt: ## Format all Go source
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Tidy module dependencies
	$(GO) mod tidy

.PHONY: demo
demo: install ## Render the terminal demo GIF (needs vhs: brew install vhs)
	@command -v vhs >/dev/null 2>&1 || { echo "vhs not installed — run: brew install vhs"; exit 1; }
	vhs demo.tape
	@echo "wrote docs/demo.gif"

.PHONY: snapshot
snapshot: ## Build a local cross-platform snapshot via goreleaser
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out
