# Three-Member Test Collective

This example demonstrates a local three-member collective setup using Docker Compose, ideal for development and testing.

## Architecture

The setup includes:
- 3 Coordinators (Alice, Bob, Carol) with automatic mesh peering
- 6 Storage Nodes (2 per member) with 50GB capacity each
- Full mTLS authentication with auto-generated certificates

## Quick Start

```bash
# From the collective root directory
docker-compose -f examples/three-member/docker-compose.yml up -d --build

# Check collective status
./bin/collective status --coordinator localhost:8001

# View logs
docker-compose -f examples/three-member/docker-compose.yml logs -f

# Stop the collective
docker-compose -f examples/three-member/docker-compose.yml down
```

## Configuration

The setup uses configuration files in the `configs/` directory for each component. These configurations specify:
- Member IDs and node IDs
- Network addresses and ports
- Storage capacities
- Bootstrap peers for automatic mesh formation

## Testing

This setup is perfect for:
- Development and debugging
- Integration testing
- Performance benchmarking
- Learning the collective architecture

## Network Layout

```
Alice (8001)  ←→  Bob (8002)
      ↓              ↓
  Nodes 1-2      Nodes 3-4
      ↑              ↑
       Carol (8003)
           ↓
        Nodes 5-6
```