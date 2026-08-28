GO      ?= go
BIN     := bin/deploymate
TEMPL   := $(shell go env GOPATH)/bin/templ

.PHONY: build gen dev test vet e2e clean

build: gen
	$(GO) build -o $(BIN) ./cmd/deploymate

# Regenerate .templ → _templ.go files.
gen:
	$(TEMPL) generate

# Run against local Docker Desktop (or a remote DOCKER_HOST) with a dev data dir.
# Port 8090: Docker Desktop occupies 8080 on macOS. Railpack resolves from
# $(go env GOPATH)/bin by default (install: see docs/knowledge/dev-environment.md).
dev: gen
	DEPLOYMATE_ADDR=127.0.0.1:8090 DEPLOYMATE_DATA_DIR=./data \
	DEPLOYMATE_RAILPACK="$(shell go env GOPATH)/bin/railpack" \
	go run ./cmd/deploymate serve

test: gen
	$(GO) test ./...

vet:
	$(GO) vet ./...

# End-to-end API smoke test (phases P2+ extend this).
e2e: build
	./testdata/e2e.sh

clean:
	rm -rf bin data
