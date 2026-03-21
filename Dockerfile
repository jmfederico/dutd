# syntax=docker/dockerfile:1

# ── Stage 1: build ──────────────────────────────────────────────────────────
# BuildKit sets TARGETPLATFORM / TARGETARCH automatically when building
# multi-platform images with --platform, so no manual GOARCH wiring is needed.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

# Build arguments injected by BuildKit for cross-compilation.
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /src

# Cache dependency downloads separately from the source build.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build a fully static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags="-s -w" \
      -trimpath \
      -o /out/dutd \
      .

# ── Stage 2: runtime ────────────────────────────────────────────────────────
# distroless/static provides an absolute minimum attack surface:
# no shell, no package manager, no OS utilities.
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/dutd /dutd

# The Docker socket must be bind-mounted at runtime; nothing is baked in.
# Example: -v /var/run/docker.sock:/var/run/docker.sock

ENTRYPOINT ["/dutd"]
