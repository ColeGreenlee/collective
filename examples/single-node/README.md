# Single-Node Development Setup

This example shows the simplest possible collective setup - a single member with one coordinator and two storage nodes, perfect for local development.

## Quick Start

### Using Configuration Files

```bash
# Copy example configs
cp configs/*.json /tmp/

# Start coordinator
./bin/collective coordinator -c /tmp/coordinator.json &

# Start storage nodes
./bin/collective node -c /tmp/node-01.json &
./bin/collective node -c /tmp/node-02.json &

# Check status
./bin/collective status --coordinator localhost:8001
```

### Using Command-Line Flags

```bash
# Start coordinator
./bin/collective coordinator \
  --member-id alice \
  --address :8001 &

# Start first node
./bin/collective node \
  --member-id alice \
  --node-id alice-node-01 \
  --coordinator localhost:8001 \
  --capacity 100GB \
  --address :7001 &

# Start second node
./bin/collective node \
  --member-id alice \
  --node-id alice-node-02 \
  --coordinator localhost:8001 \
  --capacity 100GB \
  --address :7002 &

# Check status
./bin/collective status --coordinator localhost:8001
```

## Configuration

See the `configs/` directory for example JSON configuration files that can be customized for your needs.

## Use Cases

This setup is ideal for:
- Local development and testing
- Learning the collective architecture
- Debugging storage operations
- Testing client applications

## Next Steps

Once comfortable with the single-node setup, try:
- The three-member example for testing distributed coordination
- The homelab-peering example for production deployment