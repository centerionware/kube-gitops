# ============================================================
# Stage 1: Build
# Compile for the target platform without pinning to a specific
# OS/arch — Docker BuildKit passes TARGETOS / TARGETARCH
# automatically when --platform is supplied (or defaults to
# the builder's native platform).
# ============================================================
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Cache module downloads separately from source compilation
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source
COPY . .

# Build a fully static binary for the target platform.
# CGO_ENABLED=0 is required for scratch to work.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/kube-gitops ./main.go

# ============================================================
# Stage 2: Runtime
# Nothing but the binary. No shell, no libc, no attack surface.
# ============================================================
FROM scratch

# Pull in CA certificates so HTTPS calls to git platforms work
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the compiled binary
COPY --from=builder /out/kube-gitops /kube-gitops

ENTRYPOINT ["/kube-gitops"]