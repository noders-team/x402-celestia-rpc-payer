# x402-celestia-rpc-payer — the paying client of an x402 Celestia RPC sidecar.

BINARY  ?= x402-celestia-rpc-payer
PKG     := ./cmd/$(BINARY)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build install test race cover vet fmt fmt-check lint tidy check \
        address balance prices run docker clean help

all: build

## build: compile the binary into ./bin
build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)
	@echo "built bin/$(BINARY) $(VERSION)"

## install: install the binary into $(go env GOPATH)/bin
install:
	go install -ldflags "$(LDFLAGS)" $(PKG)

## test: run the tests with the race detector
test:
	go test -race -count=1 ./...

## cover: run the tests and write coverage.txt
cover:
	go test -race -count=1 -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -func=coverage.txt | tail -1

## vet: run go vet
vet:
	go vet ./...

## fmt: format the source
fmt:
	gofmt -s -w .

## fmt-check: fail when a file is not formatted
fmt-check:
	@files=$$(gofmt -s -l .); \
	if [ -n "$$files" ]; then echo "not formatted:"; echo "$$files"; exit 1; fi

## lint: run golangci-lint. It needs no install, and CI uses the same version.
#
# CAUTION: golangci-lint refuses a module whose Go version is above the Go
# version that built the linter. Raise LINT_VERSION with the "go" line of
# go.mod, and raise it in .github/workflows/ci.yml too.
LINT_VERSION ?= v2.12.2
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION) run ./...

## tidy: clean go.mod and go.sum
tidy:
	go mod tidy

## check: read config.yaml and print it
check: build
	./bin/$(BINARY) -config config.yaml check

## address: print the address of the payer wallet
address: build
	./bin/$(BINARY) -config config.yaml address

## balance: print the balance of the payer wallet
balance: build
	./bin/$(BINARY) -config config.yaml balance

## prices: print the price of each method of the sidecar
prices: build
	./bin/$(BINARY) -config config.yaml prices

## run: start the local proxy with config.yaml
run: build
	./bin/$(BINARY) -config config.yaml serve

## docker: build the container image
docker:
	docker build -t $(BINARY):$(VERSION) .

## clean: remove the build output
clean:
	rm -rf bin dist coverage.txt

help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
