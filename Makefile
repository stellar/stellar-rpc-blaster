STELLAR_RPC_BLASTER_BINARY := stellar-rpc-blaster
CMD_PKG := ./cmd/stellar-rpc-blaster
BIN := $(STELLAR_RPC_BLASTER_BINARY)

REPOSITORY_COMMIT_HASH := "$(shell git rev-parse HEAD)"
ifeq (${REPOSITORY_COMMIT_HASH},"")
    $(error failed to retrieve git head commit hash)
endif
# Want to treat empty assignment, `REPOSITORY_VERSION=` the same as absence or unset.
# By default make `?=` operator will treat empty assignment as a set value and will not use the default value.
# Both cases should fallback to default of getting the version from git tag.
ifeq ($(strip $(REPOSITORY_VERSION)),)
	override REPOSITORY_VERSION = "$(shell git describe --tags --always --abbrev=0 --match='v[0-9]*.[0-9]*.[0-9]*' 2> /dev/null | sed 's/^.//')"
endif
REPOSITORY_BRANCH := "$(shell git rev-parse --abbrev-ref HEAD)"
ifeq ($(shell command -v jq 2>/dev/null),)
    $(error if no jq then no version at compile time)
endif

BUILD_TIMESTAMP ?= $(shell date '+%Y-%m-%dT%H:%M:%S')
GOLDFLAGS :=	-X 'github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config.Version=${REPOSITORY_VERSION}' \
				-X 'github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config.CommitHash=${REPOSITORY_COMMIT_HASH}' \
				-X 'github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config.BuildTimestamp=${BUILD_TIMESTAMP}' \
				-X 'github.com/stellar/stellar-rpc-blaster/cmd/stellar-rpc-blaster/internal/config.Branch=${REPOSITORY_BRANCH}'

.PHONY: all build run test lint fmt tidy clean

all: build-rpc-blaster

build: build-rpc-blaster

build-rpc-blaster:
	go build -ldflags="${GOLDFLAGS}" -o ${STELLAR_RPC_BLASTER_BINARY} -trimpath -v ${CMD_PKG}

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
