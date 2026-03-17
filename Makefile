.PHONY: build build-install install test clean

BINARY := agency
BUILD_DIR := ./bin
CMD := ./cmd/agency
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY) $(CMD)

build-install:
	go build -ldflags "$(LDFLAGS)" -o $(HOME)/bin/$(BINARY) $(CMD)

install:
	go install -ldflags "$(LDFLAGS)" $(CMD)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
