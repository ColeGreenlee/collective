#!/bin/bash
# Jules environment setup script for Collective federated storage system
# This script prepares the development environment for Jules AI

set -e  # Exit on error

echo "🚀 Setting up Collective development environment for Jules..."

# Check Go version
go version || { echo "❌ Go is not installed"; exit 1; }

# Download Go dependencies
echo "📦 Installing Go dependencies..."
go mod download

# Install protobuf Go plugins for code generation
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Tidy modules
go mod tidy

# Build the binary to ensure everything compiles
echo "🔨 Building collective binary..."
go build -o bin/collective ./cmd/collective/

# Run formatting check and auto-fix if needed
if [ "$(gofmt -s -l . | wc -l)" -gt 0 ]; then
    echo "🎨 Formatting code..."
    gofmt -s -w .
fi

# Run go vet
echo "🔍 Running go vet..."
go vet ./pkg/... && go vet ./cmd/collective/

# Run tests (non-blocking to allow setup to continue)
echo "🧪 Running tests..."
if go test -race -short ./pkg/...; then
    echo "✅ All tests passed"
else
    echo "⚠️  Some tests had issues but build succeeded - Jules can proceed"
fi

# Create necessary directories
mkdir -p bin/
mkdir -p /tmp/collective-test

echo "✅ Collective environment ready for Jules!"
echo ""
echo "Quick commands:"
echo "  Build: go build -o bin/collective ./cmd/collective/"
echo "  Test:  go test -race -short ./pkg/..."
echo "  Run:   ./bin/collective --help"