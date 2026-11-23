# Uses only GNU Make 3.81 features (no .ONESHELL, .RECIPEPREFIX, `!=`,
# grouped targets or `undefine`) so it runs on any stock make, old or new.
# CI calls these same targets.

GOLANGCI_LINT_VERSION := v2.15.0

BIN           := $(CURDIR)/bin
GOLANGCI_LINT := $(BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

INGESTOR := services/ingestor
DETECTOR := services/detector
COMPOSE  := docker compose -f deploy/docker-compose.yml

.DEFAULT_GOAL := help

.PHONY: help lint lint-go lint-py test test-go test-py fmt up down gen tools

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-10s %s\n", $$1, $$2}'

lint: lint-go lint-py ## Run all linters

test: test-go test-py ## Run all tests

lint-go: $(GOLANGCI_LINT) ## Lint Go (golangci-lint: linters + gofumpt/goimports)
	cd $(INGESTOR) && $(GOLANGCI_LINT) run ./...

test-go: ## Run Go tests with the race detector
	cd $(INGESTOR) && go test -race ./...

lint-py: ## Lint Python (ruff check, ruff format --check, ty)
	cd $(DETECTOR) && uv run --locked ruff check .
	cd $(DETECTOR) && uv run --locked ruff format --check .
	cd $(DETECTOR) && uv run --locked ty check

test-py: ## Run Python tests
	cd $(DETECTOR) && uv run --locked pytest

fmt: $(GOLANGCI_LINT) ## Auto-format Go and Python code
	cd $(INGESTOR) && $(GOLANGCI_LINT) fmt ./...
	cd $(DETECTOR) && uv run --locked ruff check --fix .
	cd $(DETECTOR) && uv run --locked ruff format .

up: ## Start the local stack (waits for healthchecks)
	$(COMPOSE) up -d --wait

down: ## Stop the local stack
	$(COMPOSE) down

gen: ## Generate code from protobuf definitions
	@echo "nothing to generate yet (protobuf codegen arrives in M1)"

tools: $(GOLANGCI_LINT) ## Install pinned tool binaries into ./bin

# The version is part of the file name, so bumping GOLANGCI_LINT_VERSION triggers a fresh install.
$(GOLANGCI_LINT):
	mkdir -p $(BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh \
		| sh -s -- -b $(BIN) $(GOLANGCI_LINT_VERSION)
	mv $(BIN)/golangci-lint $(GOLANGCI_LINT)
