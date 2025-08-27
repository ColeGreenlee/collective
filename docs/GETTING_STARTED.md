# Getting Started

## Prerequisites

- Go 1.23 or higher
- Protocol Buffers compiler (protoc)
- Docker and Docker Compose (for containerized deployment)
- FUSE support (Linux/macOS) for filesystem mounting

## Building from Source

```bash
# Clone the repository
git clone https://github.com/yourusername/collective.git
cd collective

# Install dependencies
make deps

# Generate protobuf files
make proto

# Build the binary
make build
```

## Quick Start with Docker

The fastest way to get started is using Docker Compose with our three-member test collective:

```bash
# Build and start the test collective
docker-compose up -d --build

# Check status of the collective
./bin/collective status --coordinator alice:8001

# View logs
docker-compose logs -f

# Stop all services
docker-compose down
```

## First-Time Client Setup

Initialize your client configuration to connect to a collective:

```bash
# Interactive setup
./bin/collective init --interactive

# Or directly specify connection details
./bin/collective init \
  --name mylab \
  --coordinator homelab.local:8001 \
  --member alice \
  --ca-cert /path/to/ca.crt

# Check your contexts
./bin/collective config get-contexts

# View detailed authentication status
./bin/collective config show-context mylab
```

## Basic Operations

### Status Monitoring

```bash
# Check collective status (uses current context)
./bin/collective status

# JSON output for scripting
./bin/collective status --json
```

### Directory Operations

```bash
# Create directories
./bin/collective mkdir /documents

# List directory contents  
./bin/collective ls /documents

# Move/rename directories
./bin/collective mv /documents /archive

# Remove directories
./bin/collective rm /archive
```

### Filesystem Mounting (Linux/Mac)

```bash
# Mount the collective storage
./bin/collective mount /mnt/collective &

# Use standard filesystem operations
echo "Hello World" > /mnt/collective/test.txt
mkdir /mnt/collective/photos
ls -la /mnt/collective/

# Unmount when done
fusermount -u /mnt/collective  # Linux
umount /mnt/collective         # macOS
```

## Running Your Own Collective

### Local Development Setup

For local development, you can run a single-member collective:

```bash
# Start coordinator
./bin/collective coordinator \
  --member-id alice \
  --address :8001 &

# Start storage nodes
./bin/collective node \
  --member-id alice \
  --node-id alice-node-01 \
  --coordinator localhost:8001 \
  --capacity 100GB &

./bin/collective node \
  --member-id alice \
  --node-id alice-node-02 \
  --coordinator localhost:8001 \
  --capacity 100GB &

# Check status
./bin/collective status --coordinator localhost:8001
```

### Multi-Member Setup

For a production multi-member collective, see the examples in:
- `examples/three-member/` - Local three-member test setup
- `examples/homelab-peering/` - Cross-network homelab deployment

## Configuration

Configuration can be provided via JSON files or command-line flags.

### Using Configuration Files

```bash
# Run with config file
./bin/collective coordinator -c configs/coordinator.json
./bin/collective node -c configs/node.json
```

### Sample Coordinator Config

```json
{
  "mode": "coordinator",
  "member_id": "alice",
  "coordinator": {
    "address": ":8001",
    "bootstrap_peers": [
      {
        "member_id": "bob",
        "address": "bob.example.com:8001"
      }
    ],
    "data_dir": "./data/alice-coord"
  }
}
```

### Sample Node Config

```json
{
  "mode": "node",
  "member_id": "alice",
  "node": {
    "node_id": "alice-node-01",
    "address": ":7001",
    "coordinator_address": "localhost:8001",
    "storage_capacity": "100GB",
    "data_dir": "./data/alice-node-01"
  }
}
```

## Next Steps

- Explore the `examples/` directory for different deployment scenarios
- Read the [Architecture documentation](ARCHITECTURE.md) to understand the system design
- Check [Performance benchmarks](PERFORMANCE.md) for optimization tips
- See [FUSE documentation](FUSE.md) for filesystem mounting details