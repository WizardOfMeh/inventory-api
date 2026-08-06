# syntax=docker/dockerfile:1

# ---- Build stage -------------------------------------------------------
# Pinned to the current supported Go line. Only the two latest majors
# receive security patches, so this is bumped deliberately, not left behind.
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Dependencies first: this layer is only invalidated when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Cache mounts keep the module and build caches between builds without
# putting them into the image.
#   CGO_ENABLED=0  -> fully static binary, runs on scratch/distroless
#   -trimpath      -> no absolute build-machine paths inside the binary
#   -w -s          -> strip DWARF and symbol table
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-w -s -X main.version=${VERSION}" \
        -o /out/api ./cmd/api

# ---- Runtime stage -----------------------------------------------------
# distroless/static: no shell, no package manager, no libc — just the
# binary, ca-certificates and tzdata. ~2 MB base.
#
# Trade-off: there is nothing to exec into for debugging. That is handled
# by ephemeral containers instead:
#   kubectl debug -it <pod> --image=busybox --target=api
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/api /app/api

# 65532 is the "nonroot" user baked into distroless. Numeric UID (not a
# name) is required for Kubernetes runAsNonRoot to verify it at admission.
USER 65532:65532

EXPOSE 8080
ENTRYPOINT ["/app/api"]
