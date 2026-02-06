STELLAR_RPC_BLASTER_BINARY := stellar-rpc-blaster
CMD_PKG := .

# Default output dir
BIN := $(STELLAR_RPC_BLASTER_BINARY)

.PHONY: all build run test lint fmt tidy clean

all: build-rpc-blaster

build: build-rpc-blaster

build-rpc-blaster:
	make clean && go build -o $(BIN) $(CMD_PKG)
# 	go build -ldflags="${GOLDFLAGS}" -o ${STELLAR_RPC_BLASTER_BINARY} -trimpath -v .

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
