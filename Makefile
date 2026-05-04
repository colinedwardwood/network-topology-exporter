SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

BINARY      := topology-exporter
PKG         := github.com/owner-tbd/network-topology-exporter
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE        := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
               -X $(PKG)/internal/version.Version=$(VERSION) \
               -X $(PKG)/internal/version.Commit=$(COMMIT)   \
               -X $(PKG)/internal/version.BuildDate=$(DATE)

.PHONY: help
help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary into bin/
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/topology-exporter

.PHONY: run
run: build ## Build and run with the example config
	./bin/$(BINARY) --config.file=config/example.yaml

.PHONY: test
test: ## Run unit tests
	go test ./... -race -count=1

.PHONY: cover
cover: ## Generate coverage report
	go test ./... -race -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint: ## golangci-lint
	@command -v golangci-lint >/dev/null || { echo "install: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run ./...

.PHONY: fmt
fmt: ## Format
	gofmt -s -w .
	@command -v goimports >/dev/null && goimports -w . || true

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: docker
docker: ## Build the container image
	docker build -t network-topology-exporter:$(VERSION) -t network-topology-exporter:dev .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out coverage.html
