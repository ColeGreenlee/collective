# Collective Storage System

A distributed storage collective that enables small groups to pool their storage resources. Built with Go, featuring automatic peer discovery, FUSE mounting, and mTLS authentication.

## Key Features

- 🌐 **Federated Architecture** - Each member runs their own coordinator with automatic mesh networking
- 💾 **Distributed Storage** - Pool storage across multiple nodes with chunk-based distribution
- 🔐 **Zero-Trust Security** - Ed25519 mTLS authentication with per-member certificate authorities  
- 📁 **FUSE Filesystem** - Mount collective storage as a local filesystem (Linux/macOS)
- ⚡ **High Performance** - 240+ MB/s transfers with <30MB memory footprint
- 🎯 **Simple Deployment** - Single binary, Docker support, human-friendly configuration

## Quick Start

```bash
# Using Docker (recommended)
docker run -d -p 8001:8001 ghcr.io/colegreenlee/collective:latest coordinator --member-id alice

# Using Docker Compose
docker compose -f examples/three-member/docker-compose.yml up -d
./bin/collective status --coordinator localhost:8001

# Or build from source
make build
./bin/collective init --interactive  # Set up client auth
./bin/collective status              # Check collective health
```

See [Getting Started](docs/GETTING_STARTED.md) for detailed setup instructions.

## Core Operations

```bash
# File operations
./bin/collective mkdir /documents
./bin/collective ls /
echo "Hello" | ./bin/collective write /documents/hello.txt

# Mount as filesystem (Linux/macOS)
./bin/collective mount /mnt/collective
cp ~/files/* /mnt/collective/documents/

# Monitor collective
./bin/collective status --json  # Structured output for automation
```

## Examples

- [`examples/single-node/`](examples/single-node/) - Single member development setup
- [`examples/three-member/`](examples/three-member/) - Local testing with three members  
- [`examples/homelab-peering/`](examples/homelab-peering/) - Cross-network homelab deployment
- [`examples/scale-testing/`](examples/scale-testing/) - Large-scale testing tools

## Documentation

- [Getting Started](docs/GETTING_STARTED.md) - Installation and basic usage
- [Architecture](docs/ARCHITECTURE.md) - System design and components
- [FUSE Filesystem](docs/FUSE.md) - Filesystem mounting details

## Project Status

### ✅ Production Ready
- Distributed coordination with automatic peering
- Chunk-based storage with parallel transfers  
- FUSE filesystem with full POSIX operations
- mTLS authentication with Ed25519 certificates
- 100% success rate for concurrent operations
- Tested with 50+ coordinators and 500+ nodes

### 🚧 In Development
- Node heartbeats and automatic recovery
- Erasure coding for storage efficiency
- Cross-member chunk sharing

### 📋 Roadmap
- S3-compatible API
- Web management UI
- Persistent metadata storage
- Coordinator redundancy

## Requirements

- **Go**: 1.23 or higher (for building from source)
- **Docker**: For containerized deployment
- **FUSE**: For filesystem mounting (Linux/macOS)
- **Protobuf**: For regenerating protocol definitions