BINARY_NAME := stellar-rpc-blaster
CMD_PKG := .

# Default output dir (keep repo tidy)
BIN := $(BINARY_NAME)

.PHONY: all build run test lint fmt tidy clean

all: build

build:
	go build -o $(BIN) $(CMD_PKG)

run: build
	$(BIN) $(ARGS)

test:
	go test ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
