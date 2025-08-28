# Build stage
FROM ghcr.io/rblaine95/golang:1.23-alpine AS builder

RUN apk add --no-cache git make protoc protobuf protobuf-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download && \
    go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2 && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.3.0

COPY . .
RUN protoc --go_out=. --go_opt=paths=source_relative \
           --go-grpc_out=. --go-grpc_opt=paths=source_relative \
           proto/*.proto && \
    mkdir -p pkg/protocol && \
    mv proto/*.pb.go pkg/protocol/ && \
    go build -o collective ./cmd/collective/

# Runtime stage
FROM ghcr.io/rblaine95/alpine:latest

# Install ca-certificates and bash for entrypoint script
RUN apk add --no-cache ca-certificates bash

WORKDIR /app

# Copy the binary and entrypoint script
COPY --from=builder /app/collective /usr/local/bin/collective
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

# Create necessary directories
RUN mkdir -p /data /collective/certs && \
    chmod +x /usr/local/bin/docker-entrypoint.sh

# Environment variables for auto-configuration
ENV COLLECTIVE_AUTO_TLS=true \
    COLLECTIVE_CERT_DIR=/collective/certs \
    COLLECTIVE_MEMBER_ID="" \
    COLLECTIVE_COMPONENT_TYPE="" \
    COLLECTIVE_COMPONENT_ID="" \
    COLLECTIVE_COORDINATOR_ADDRESS="" \
    COLLECTIVE_STORAGE_CAPACITY=10737418240

# Expose ports
# Coordinator port (gRPC)
EXPOSE 8001
# Node port (gRPC)
EXPOSE 9001

# Volume for certificates (shared between containers)
VOLUME /collective/certs

# Volume for data (per-container)
VOLUME /data

# Use our custom entrypoint
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]

# Default command (can be overridden)
CMD ["coordinator"]