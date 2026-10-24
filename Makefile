SHELL := /bin/sh
.NOTPARALLEL:
GO := sh scripts/go.sh
GO_PACKAGES := ./cmd/... ./internal/... ./db/... ./tests/... ./scripts/...
CONFIG ?= mass.yaml
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
REVISION := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
export SOURCE_DATE_EPOCH
BUILD_TIME := $(SOURCE_DATE_EPOCH)
export NPM_CONFIG_CACHE := $(CURDIR)/.cache/npm
export PLAYWRIGHT_BROWSERS_PATH := $(CURDIR)/.cache/playwright

.PHONY: setup setup-browser generate generate-check fmt lint typecheck test-unit test-race test-e2e build-frontend dev build verify-d00 verify-d01 verify-d02 verify-d03 verify-d04 verify-d05 verify-d06 verify-d08 verify-d09-local test-fault-e2e test-debug-e2e test-context-e2e test-tools-e2e smoke-real smoke-tos test-runtime-e2e test-catalog-e2e test-integration verify release test-delivery verify-d10-local check-toolchain test-recovery smoke-recovery verify-live
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
	CGO_ENABLED=0 $(GO) build -mod=readonly -buildvcs=false -trimpath -ldflags '-X main.version=$(VERSION) -X main.revision=$(REVISION) -X main.buildTime=$(BUILD_TIME)' -o bin/mass ./cmd/mass

check-toolchain:
	node scripts/check-toolchain.mjs

release: check-toolchain generate-check build
	$(GO) run ./scripts/release

test-delivery: release
	node scripts/delivery-smoke.mjs

test-recovery: release
	MASS_TEST_RECOVERY=1 MASS_TEST_RECOVERY_TOS=0 $(GO) test -count=1 -run '^TestTOSRecovery$$' -v ./tests/integration

smoke-recovery: release
	MASS_TEST_RECOVERY=1 MASS_TEST_RECOVERY_TOS=1 $(GO) test -count=1 -timeout=15m -run '^TestTOSRecovery$$' -v ./tests/integration

verify-d10-local: export MASS_TEST_RECOVERY_TOS := 0
verify-d10-local: export MASS_TEST_TOS := 0
verify-d10-local: export MASS_TEST_REAL_MODEL := 0
verify-d10-local: verify-d09-local test-delivery test-recovery
	git diff --check

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

test-runtime-e2e: build
	node scripts/runtime-browser-smoke.mjs

test-tools-e2e: build
	node scripts/tools-browser-smoke.mjs

test-context-e2e: build
	node scripts/context-browser-smoke.mjs

test-debug-e2e: build
	node scripts/debug-browser-smoke.mjs

test-fault-e2e: build
	node scripts/fault-browser-smoke.mjs

verify-d09-local: export MASS_TEST_TOS := 0
verify-d09-local: export MASS_TEST_REAL_MODEL := 0
verify-d09-local: verify-d08 test-fault-e2e test-race
	node scripts/check-acceptance.mjs
	git diff --check

# D07 real-provider validation remains separately gated by explicit credentials.
verify-d08: verify-d06 test-debug-e2e
	git diff --check

verify-d06: verify-d05 test-context-e2e
	git diff --check

verify-d05: verify-d04 test-tools-e2e
	git diff --check

verify-d04: verify-d03 test-runtime-e2e
	git diff --check

smoke-real: build-frontend
	@test -n "$$MASS_TEST_ADMIN_DSN" || (echo 'Set MASS_TEST_ADMIN_DSN to an isolated MariaDB server' >&2; exit 1)
	MASS_TEST_TOS=1 MASS_TEST_REAL_MODEL=1 $(GO) test -count=1 -run '^TestRealTOSStorage$$' -v ./tests/integration

verify-d03: verify-d02 test-catalog-e2e
	git diff --check

verify-d01: verify-d02 smoke-tos

verify-d02: generate-check typecheck lint test-unit test-integration test-e2e
	git diff --check

verify-d00: generate-check typecheck lint test-unit
	git diff --check

# Ordinary verification combines a fresh local suite with recorded, reviewed
# real-provider acceptance. Live revalidation is explicit and can incur charges.
verify: verify-d10-local
	node scripts/check-acceptance.mjs --full

verify-live: verify
	@test "$$MASS_VERIFY_REAL_APPROVED" = 1 || (echo 'Live verification requires explicit cloud approval; see E15.11.' >&2; exit 1)
	$(MAKE) smoke-real smoke-recovery
