FROM golang:1.22-alpine AS builder


# Install build dependencies
RUN apk add --no-cache \
    git \
    make \
    bash \
    gcc \
    musl-dev \
    ca-certificates

# Set working directory
WORKDIR /workspace

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod tidy

# Copy the entire source code
COPY . .

# Build arguments
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Build the provider binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
    -a \
    -ldflags="-w -s \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.buildDate=${BUILD_DATE}" \
    -o provider \
    ./cmd/provider

# Verify the binary
RUN chmod +x provider && ./provider --version || echo "Version command not available"

# ============================================================================
# Stage 2: Create minimal runtime image
# ============================================================================
FROM gcr.io/distroless/static:nonroot AS runtime

# Metadata
LABEL org.opencontainers.image.title="Swap Provider MongoDB" \
      org.opencontainers.image.description="Crossplane provider for MongoDB Atlas with AWS Secrets Manager integration" \
      org.opencontainers.image.vendor="svchaudhari" \
      org.opencontainers.image.source="https://github.com/svchaudhari/Swap-Provider-MongoDB" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${BUILD_DATE}"

# Copy CA certificates from builder
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary from builder
COPY --from=builder --chown=nonroot:nonroot /workspace/provider /usr/local/bin/provider

# Use non-root user
USER nonroot:nonroot

# Set entrypoint
ENTRYPOINT ["/usr/local/bin/provider"]
