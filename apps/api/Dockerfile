# Build stage
# Base images are pinned by digest; Dependabot (.github/dependabot.yml) bumps them.
# Keep the Go line in step with the toolchain directive in go.mod.
FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
ARG VERSION=development
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s -X github.com/dublyo/mailat/api/internal/controller.BuildVersion=${VERSION}" -o /app/server ./cmd/server

# Final stage
FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

WORKDIR /app

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /app/server /app/server

# Create non-root user
RUN adduser -D -g '' appuser
USER appuser

# The server's own default is 3001; the image listens on 8000 unless PORT is overridden.
ENV PORT=8000
EXPOSE 8000

CMD ["/app/server"]
