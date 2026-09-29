# syntax=docker/dockerfile:1
#
# Multi-stage build for the tx-load-test CLI.
#
# The binary is fully static (CGO disabled), so the final image is FROM scratch:
# just the binary, the TLS root certificates, and the flat Wasm artifacts that
# `setup` needs. bench / teardown / sync do not read the Wasm files.

# ---- build stage ----
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Download modules first so this layer is cached across source-only changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# Build a fully static binary. CGO_ENABLED=0 is what makes `scratch` viable;
# -trimpath + -s -w keep it lean and reproducible.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /out/tx-load-test ./cmd/tx-load-test

# ---- final stage ----
FROM scratch

# TLS roots, so HTTPS RPC endpoints and friendbot work. Harmless if you only
# ever talk to a plain-HTTP RPC.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

COPY --from=build /out/tx-load-test /usr/local/bin/tx-load-test

# Flat Wasm artifacts, resolved by `setup` relative to the working directory as
# "contracts/<name>.wasm". Only needed for `setup` on standalone/futurenet;
# bench / teardown / sync ignore them.
COPY contracts/oz_token.wasm \
     contracts/soroswap_pair.wasm \
     contracts/soroswap_factory.wasm \
     contracts/soroswap_router.wasm \
     /contracts/

WORKDIR /

# Run unprivileged. 65532 is the conventional "nonroot" uid/gid (matches
# distroless), so Kubernetes runAsNonRoot: true is satisfied without extra
# config. All writable output (metrics, trace) must go to a mounted volume;
# the root filesystem can be read-only.
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/tx-load-test"]
