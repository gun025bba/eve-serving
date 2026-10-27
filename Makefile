# Go gateway build helpers.
BIN      ?= bin/gateway
# SemVer from the nearest git tag (v1.1.0 -> 1.1.0); see docs/versioning.md.
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
HOST     ?= 127.0.0.1
PORT     ?= 8001

.PHONY: build test test-race test-integration migrate vet fmt run clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/gateway
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/eve-gw ./cmd/eve-gw
test:
	go test ./...
test-race:
	go test -race ./...
# Postgres integration tests against llmchat_test (GW_TEST_DATABASE_URL from .db_env).
test-integration:
	set -a; . ./.db_env; set +a; go test -race -count=1 ./internal/store/postgres/
# Apply embedded goose migrations to GW_DATABASE_URL (also run at startup unless GW_DB_AUTO_MIGRATE=false).
migrate:
	set -a; . ./.db_env; set +a; go run ./cmd/gateway -migrate
vet:
	go vet ./...
fmt:
	gofmt -w cmd internal
run: build
	./$(BIN) -host $(HOST) -port $(PORT)
clean:
	rm -rf bin
