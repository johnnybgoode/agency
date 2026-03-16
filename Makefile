.PHONY: build build-install install test clean

BINARY := agency
BUILD_DIR := ./bin
CMD := ./cmd/agency

build:
	go build -o $(BUILD_DIR)/$(BINARY) $(CMD)

build-install:
	go build -o $(HOME)/bin/$(BINARY) $(CMD)

install:
	go install $(CMD)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
