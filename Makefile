.PHONY: build install test clean

BINARY := agency
BUILD_DIR := ./bin
CMD := ./cmd/agency

build:
	go build -o $(BUILD_DIR)/$(BINARY) $(CMD)

install:
	go install $(CMD)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
