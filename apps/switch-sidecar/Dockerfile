# Build stage
FROM golang:1.21-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go module files and download dependencies first (for layer caching).
COPY go.mod go.sum ./
RUN go mod download

# Copy source code.
COPY . .

# Build the binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -o /switch-sidecar \
    ./cmd/sidecar/

# Runtime stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata curl

COPY --from=builder /switch-sidecar /usr/local/bin/switch-sidecar

# Health/metrics port.
EXPOSE 8082

USER 65534:65534

ENTRYPOINT ["/usr/local/bin/switch-sidecar"]
