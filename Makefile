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
	DEPLOYMATE_PREVIEW_HOST=dm.getmerchanttech.com \
	go run ./cmd/deploymate serve

test: gen
	$(GO) test ./...

vet:
	$(GO) vet ./...

# End-to-end API smoke test (image deploy) against a running server.
e2e: build
	./testdata/e2e.sh

# End-to-end git-deploy path on a self-contained throwaway server: signed
# webhook -> worker clone/build/swap -> preview-proxy probe. Builds its own
# binary + server + local repo and cleans everything up. Safe to run anytime.
e2e-git:
	./testdata/e2e_git.sh

clean:
	rm -rf bin data
