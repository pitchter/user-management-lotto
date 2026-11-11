# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Stage 1: build
#
# Building in a separate stage keeps the Go toolchain, the source code and
# the build cache out of the image that actually ships.
# ---------------------------------------------------------------------------
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Copy only the dependency files first and download: this layer is cached and
# is not redone when only the code changes, which makes later builds far faster.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG VERSION=docker
ARG COMMIT=none
ARG BUILD_DATE=unknown

# CGO_ENABLED=0 produces a static binary that runs on an image without libc.
# -trimpath strips the build machine's paths (reproducible builds, and no
# leaking of the build host's directory layout).
# -s -w drop the symbol table and debug info, making the binary smaller.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}" \
    -o /out/api ./cmd/api

# ---------------------------------------------------------------------------
# Stage 2: the image that actually runs
#
# distroless/static carries only CA certificates, time zone data and
# /etc/passwd: no shell, no package manager, no utilities, so an attacker who
# gets in finds no tools to go on with, and the image stays tiny.
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static:nonroot

# Run as a non-root user, so a vulnerability does not hand over root.
USER nonroot:nonroot

COPY --from=builder /out/api /api

EXPOSE 8080 50051

# The binary health-checks itself, because this image has no curl or wget.
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/api", "healthcheck"]

ENTRYPOINT ["/api"]
