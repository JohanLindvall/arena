.DEFAULT_GOAL := help

GO ?= go
GOLANGCI_LINT ?= golangci-lint

.PHONY: help test fmt check coverage fuzz bench

help:
	@printf '%s\n' \
		'make test      Run race tests, examples, and fuzz seeds' \
		'make fmt       Format Go code with gofmt and goimports' \
		'make check     Run the local CI checks in order' \
		'make coverage  Write coverage.out and report coverage' \
		'make fuzz      Fuzz mixed arena operations for 20 seconds' \
		'make bench     Run benchmarks five times with allocation counts'

test:
	$(GO) test -race -shuffle=on -count=1 ./...

fmt:
	$(GOLANGCI_LINT) fmt

# Keep these sequential even under make -j: a failed check stops the recipe.
# Keep the commands aligned with .github/workflows/ci.yml and CONTRIBUTING.md.
check:
	$(GO) build ./...
	$(GO) vet ./...
	$(GO) test -race -shuffle=on -count=1 ./...
	$(GO) mod tidy -diff
	$(GOLANGCI_LINT) run
	$(GO) test -run='^$$' -bench=. -benchtime=1x ./...

coverage:
	$(GO) test -race -covermode=atomic -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

fuzz:
	$(GO) test -fuzz=FuzzArena -fuzztime=20s ./...

bench:
	$(GO) test -run='^$$' -bench=. -benchmem -count=5 ./...
