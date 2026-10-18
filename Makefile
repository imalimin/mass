SHELL := /bin/sh
.NOTPARALLEL:
GO := sh scripts/go.sh
GO_PACKAGES := ./cmd/... ./internal/... ./db/... ./tests/... ./scripts/...
CONFIG ?= mass.yaml
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
export NPM_CONFIG_CACHE := $(CURDIR)/.cache/npm
export PLAYWRIGHT_BROWSERS_PATH := $(CURDIR)/.cache/playwright

.PHONY: setup setup-browser generate generate-check fmt lint typecheck test-unit test-race test-e2e build-frontend dev build verify-d00 verify-d01 verify-d02 verify-d03 smoke-tos test-catalog-e2e test-integration verify
setup:
	node scripts/check-toolchain.mjs
	$(GO) mod download
	npm --prefix frontend ci
	$(MAKE) setup-browser

setup-browser:
	npm --prefix frontend exec -- playwright install $(PLAYWRIGHT_INSTALL_FLAGS) chromium

generate: build-frontend
	node scripts/generate.mjs

generate-check: build-frontend
	node scripts/generate.mjs --check

build-frontend:
	npm --prefix frontend run build
	node scripts/embed.mjs

fmt:
	$(GO) fmt $(GO_PACKAGES)

typecheck:
	npm --prefix frontend run typecheck

lint: build-frontend
	$(GO) vet $(GO_PACKAGES)
	npm --prefix frontend run lint
	node scripts/check-boundaries.mjs

test-unit: build-frontend
	$(GO) test $(GO_PACKAGES)
	npm --prefix frontend test

test-race: build-frontend
	$(GO) test -race $(GO_PACKAGES)

build: build-frontend
	$(GO) build -trimpath -ldflags '-X main.version=$(VERSION)' -o bin/mass ./cmd/mass

test-e2e: build
	node scripts/smoke.mjs
	node scripts/browser-smoke.mjs

dev:
	node scripts/dev.mjs $(CONFIG)

test-integration: build-frontend
	@test -n "$$MASS_TEST_ADMIN_DSN" || (echo 'Set MASS_TEST_ADMIN_DSN to an isolated MariaDB server' >&2; exit 1)
	$(GO) test -count=1 ./tests/integration/...

smoke-tos: build-frontend
	@test -n "$$MASS_TEST_ADMIN_DSN" || (echo 'Set MASS_TEST_ADMIN_DSN to an isolated MariaDB server' >&2; exit 1)
	MASS_TEST_TOS=1 $(GO) test -count=1 -run '^TestRealTOSStorage$$' -v ./tests/integration

test-catalog-e2e: build
	node scripts/catalog-browser-smoke.mjs

verify-d03: verify-d02 test-catalog-e2e
	git diff --check

verify-d01: verify-d02 smoke-tos

verify-d02: generate-check typecheck lint test-unit test-integration test-e2e
	git diff --check

verify-d00: generate-check typecheck lint test-unit
	git diff --check

# Full MVP verification must not claim success while later stages are absent.
verify:
	@echo 'Full MVP verify is unavailable before D10; use make verify-d03 for definition/version management.' >&2
	@exit 1
