# Uses only GNU Make 3.81 features (no .ONESHELL, .RECIPEPREFIX, `!=`,
# grouped targets or `undefine`) so it runs on any stock make, old or new.
# CI calls these same targets.

GOLANGCI_LINT_VERSION := v2.15.0
BUF_VERSION           := v1.75.0
# Keep in step with the Python `protobuf` runtime (7.<PROTOC_VERSION>) in services/detector.
PROTOC_VERSION        := 36.2

UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

BIN           := $(CURDIR)/bin
GOLANGCI_LINT := $(BIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
BUF           := $(BIN)/buf-$(BUF_VERSION)
# buf.gen.yaml points at bin/protoc/bin/protoc; the stamp file makes a version bump reinstall it.
PROTOC_DIR    := $(BIN)/protoc
PROTOC_STAMP  := $(PROTOC_DIR)/.version-$(PROTOC_VERSION)
ifeq ($(UNAME_S),Darwin)
PROTOC_PLATFORM := osx-universal_binary
else
PROTOC_PLATFORM := linux-$(subst aarch64,aarch_64,$(UNAME_M))
endif

INGESTOR := services/ingestor
DETECTOR := services/detector
COMPOSE  := docker compose -f deploy/docker-compose.yml

# Environment file read by `make run-ingestor`. Variables already set in the environment win.
INGESTOR_ENV ?= deploy/ingestor.env

# Output of `make gen`. Never edit these by hand.
GEN_DIRS := $(INGESTOR)/internal/gen $(DETECTOR)/src/arbscanner

.DEFAULT_GOAL := help

# Commits checked by `make check-commits`: everything not yet on origin/main by default.
RANGE ?= origin/main..HEAD

# Git ref that `make breaking` compares the protobuf schema against.
BREAKING_REF ?= main

.PHONY: help lint lint-go lint-py lint-proto test test-go test-py fmt up down run-ingestor gen breaking tools hooks check-commits

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-14s %s\n", $$1, $$2}'

lint: lint-go lint-py lint-proto ## Run all linters

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

lint-proto: $(BUF) ## Lint and format-check the protobuf schema (buf)
	$(BUF) lint
	$(BUF) format --diff --exit-code

fmt: $(GOLANGCI_LINT) $(BUF) ## Auto-format Go, Python and protobuf code
	cd $(INGESTOR) && $(GOLANGCI_LINT) fmt ./...
	cd $(DETECTOR) && uv run --locked ruff check --fix .
	cd $(DETECTOR) && uv run --locked ruff format .
	$(BUF) format --write

up: ## Start the local stack (waits for healthchecks)
	$(COMPOSE) up -d --wait

down: ## Stop the local stack
	$(COMPOSE) down

run-ingestor: ## Run the ingestor locally with INGESTOR_ENV (default deploy/ingestor.env); needs `make up`
	cd $(INGESTOR) && sh $(CURDIR)/scripts/with-env.sh $(abspath $(INGESTOR_ENV)) go run ./cmd/ingestor

gen: $(BUF) $(PROTOC_STAMP) ## Generate Go and Python code from the protobuf schema
	rm -rf $(GEN_DIRS)
	$(BUF) generate

# Skipped when BREAKING_REF has no buf.yaml yet (the first commit that adds the schema).
breaking: $(BUF) ## Check the protobuf schema for breaking changes against BREAKING_REF (default main)
	@if git cat-file -e '$(BREAKING_REF):buf.yaml' 2>/dev/null; then \
		echo "$(BUF) breaking --against '.git#ref=$(BREAKING_REF)'"; \
		$(BUF) breaking --against '.git#ref=$(BREAKING_REF)'; \
	else \
		echo "no buf.yaml at $(BREAKING_REF), nothing to compare against: skipping"; \
	fi

tools: $(GOLANGCI_LINT) $(BUF) $(PROTOC_STAMP) ## Install pinned tool binaries into ./bin

hooks: ## Enable the repo git hooks (Conventional Commits check on commit)
	git config core.hooksPath .githooks

check-commits: ## Check commit messages in RANGE (default origin/main..HEAD)
	sh scripts/check-commits.sh $(RANGE)

# The version is part of the file name, so bumping GOLANGCI_LINT_VERSION triggers a fresh install.
$(GOLANGCI_LINT):
	mkdir -p $(BIN)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh \
		| sh -s -- -b $(BIN) $(GOLANGCI_LINT_VERSION)
	mv $(BIN)/golangci-lint $(GOLANGCI_LINT)

$(BUF):
	mkdir -p $(BIN)
	curl -sSfL -o $@.tmp https://github.com/bufbuild/buf/releases/download/$(BUF_VERSION)/buf-$(UNAME_S)-$(UNAME_M)
	chmod +x $@.tmp
	mv $@.tmp $@

$(PROTOC_STAMP):
	rm -rf $(PROTOC_DIR)
	mkdir -p $(PROTOC_DIR)
	curl -sSfL -o $(PROTOC_DIR)/protoc.zip \
		https://github.com/protocolbuffers/protobuf/releases/download/v$(PROTOC_VERSION)/protoc-$(PROTOC_VERSION)-$(PROTOC_PLATFORM).zip
	unzip -q $(PROTOC_DIR)/protoc.zip -d $(PROTOC_DIR)
	rm $(PROTOC_DIR)/protoc.zip
	touch $@
