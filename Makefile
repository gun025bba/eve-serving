# Go gateway build helpers.
BIN      ?= bin/gateway
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
HOST     ?= 127.0.0.1
PORT     ?= 8001

.PHONY: build test vet fmt run clean
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/gateway
test:
	go test ./...
vet:
	go vet ./...
fmt:
	gofmt -w cmd internal
run: build
	./$(BIN) -host $(HOST) -port $(PORT)
clean:
	rm -rf bin
