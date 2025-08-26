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
	$(GO) build -o bin/$(BINARY_NAME) cmd/collective/main.go

# Run tests
test:
	$(GO) test -v ./...

# Run integration tests
integration-test:
	$(GO) test -v ./test/...

# Local development setup
run-local: build
	@echo "Starting three-member collective locally..."
	@echo "Run each command in a separate terminal:"
	@echo ""
	@echo "# Alice's coordinator:"
	@echo "./bin/$(BINARY_NAME) coordinator -c data/configs/alice-coordinator.json"
	@echo ""
	@echo "# Alice's nodes:"
	@echo "./bin/$(BINARY_NAME) node -c data/configs/alice-node-01.json"
	@echo "./bin/$(BINARY_NAME) node -c data/configs/alice-node-02.json"
	@echo ""
	@echo "# Bob's coordinator:"
	@echo "./bin/$(BINARY_NAME) coordinator -c data/configs/bob-coordinator.json"
	@echo ""
	@echo "# Bob's nodes:"
	@echo "./bin/$(BINARY_NAME) node -c data/configs/bob-node-01.json"
	@echo "./bin/$(BINARY_NAME) node -c data/configs/bob-node-02.json"
	@echo ""
	@echo "# Carol's coordinator:"
	@echo "./bin/$(BINARY_NAME) coordinator -c data/configs/carol-coordinator.json"
	@echo ""
	@echo "# Carol's nodes:"
	@echo "./bin/$(BINARY_NAME) node -c data/configs/carol-node-01.json"
	@echo "./bin/$(BINARY_NAME) node -c data/configs/carol-node-02.json"

# Docker commands
docker-build:
	docker-compose build

docker-up:
	docker-compose up -d

docker-down:
	docker-compose down

docker-logs:
	docker-compose logs -f

# Clean build artifacts
clean:
	rm -rf bin/
	rm -rf data/
	rm -rf test-data/
	docker-compose down -v

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