# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git make protoc

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
    go build -o collective cmd/collective/main.go

# Runtime stage
FROM alpine:latest

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/collective /app/collective
COPY --from=builder /app/configs /app/configs

# Create data directory
RUN mkdir -p /data

# Default to coordinator mode
ENV COLLECTIVE_MODE=coordinator
ENV COLLECTIVE_MEMBER_ID=""
ENV COLLECTIVE_COORDINATOR_ADDRESS=":8001"
ENV COLLECTIVE_NODE_ADDRESS=":7001"
ENV COLLECTIVE_DATA_DIR="/data"

EXPOSE 8001 8002 8003 7001 7002 7003 7004 7005 7006

ENTRYPOINT ["/app/collective"]