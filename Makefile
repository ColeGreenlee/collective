.PHONY: all build proto test run-local clean docker-build docker-up docker-down

# Build variables
BINARY_NAME=collective
GO=go
PROTOC=protoc

all: proto build

# Generate protobuf files
proto:
	$(PROTOC) --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/*.proto

# Build the binary
build:
	$(GO) build -o bin/$(BINARY_NAME) ./cmd/collective/

# Run all tests
test:
	$(GO) test -v -race -coverprofile=coverage.out ./...

# Run unit tests only
test-unit:
	$(GO) test -v -short -race ./pkg/...

# Run integration tests
test-integration:
	$(GO) test -v -race ./test/...

# Run tests with coverage
test-coverage:
	$(GO) test -v -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
	@$(GO) tool cover -func=coverage.out | tail -1

# Run benchmarks
test-benchmark:
	$(GO) test -bench=. -benchmem ./pkg/...

# Local development setup
run-local: build
	@echo "Starting federation example locally..."
	@echo "Run: cd examples/federation && docker-compose up -d --build"
	@echo ""
	@echo "Or for manual setup, run each command in a separate terminal:"
	@echo ""
	@echo "# Alice's coordinator:"
	@echo "./bin/$(BINARY_NAME) coordinator --member-id alice --address :8001"
	@echo ""
	@echo "# Alice's nodes:"
	@echo "./bin/$(BINARY_NAME) node --member-id alice --node-id alice-node-01 --capacity 5GB"
	@echo "./bin/$(BINARY_NAME) node --member-id alice --node-id alice-node-02 --capacity 5GB"
	@echo ""
	@echo "# Bob's coordinator:"
	@echo "./bin/$(BINARY_NAME) coordinator --member-id bob --address :8002"
	@echo ""
	@echo "# Bob's nodes:"
	@echo "./bin/$(BINARY_NAME) node --member-id bob --node-id bob-node-01 --capacity 5GB"
	@echo "./bin/$(BINARY_NAME) node --member-id bob --node-id bob-node-02 --capacity 5GB"
	@echo ""
	@echo "# Carol's coordinator:"
	@echo "./bin/$(BINARY_NAME) coordinator --member-id carol --address :8003"
	@echo ""
	@echo "# Carol's nodes:"
	@echo "./bin/$(BINARY_NAME) node --member-id carol --node-id carol-node-01 --capacity 5GB"
	@echo "./bin/$(BINARY_NAME) node --member-id carol --node-id carol-node-02 --capacity 5GB"

# Docker commands for federation example
docker-federation:
	cd examples/federation && docker-compose up -d --build

docker-federation-down:
	cd examples/federation && docker-compose down

docker-federation-logs:
	cd examples/federation && docker-compose logs -f

# Docker commands for basic three-member setup
docker-build:
	docker-compose -f examples/three-member/docker-compose.yml build

docker-up:
	docker-compose -f examples/three-member/docker-compose.yml up -d

docker-down:
	docker-compose -f examples/three-member/docker-compose.yml down

docker-logs:
	docker-compose -f examples/three-member/docker-compose.yml logs -f

# Clean build artifacts
clean:
	rm -rf bin/
	rm -rf data/
	docker-compose -f examples/federation/docker-compose.yml down -v 2>/dev/null || true
	docker-compose -f examples/three-member/docker-compose.yml down -v 2>/dev/null || true

# Install dependencies
deps:
	$(GO) mod download
	$(GO) mod tidy

# Format code
fmt:
	$(GO) fmt ./...

# Run linter
lint:
	golangci-lint run

# Quick local test with single member
quick-test: build
	@echo "Starting single member for quick testing..."
	./bin/$(BINARY_NAME) coordinator --member-id alice --address :8001 &
	sleep 2
	./bin/$(BINARY_NAME) node --member-id alice --node-id alice-node-01 --address :7001 --coordinator localhost:8001 &
	./bin/$(BINARY_NAME) node --member-id alice --node-id alice-node-02 --address :7002 --coordinator localhost:8001 &
	@echo "Quick test setup complete. Press Ctrl+C to stop."
	@wait