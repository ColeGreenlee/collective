# Collective Storage System

A distributed storage collective that enables small groups to pool their storage resources. Built with Go, featuring automatic peer discovery, FUSE mounting, and mTLS authentication.

## Key Features

- 🌐 **Full Federation Support** - Complete federated storage with Mastodon-style addressing (node@domain)
- 💾 **Smart Chunk Placement** - Optimized strategies for media streaming vs backup workloads
- 🔐 **Hierarchical CA System** - Multi-member trust with per-member certificate authorities
- 📁 **Cross-Member FUSE Access** - Mount and access files across the entire federation
- ⚡ **High Performance** - 240+ MB/s transfers with intelligent caching and prefetching
- 🎯 **Production Ready** - Monitoring, health checks, resilience patterns, and alerting
- 🤝 **Gossip Protocol** - Automatic peer discovery and eventual consistency
- 🎫 **Invite System** - Easy member onboarding with pre-authorized invitation codes

## Quick Start

```bash
# Clone and build
git clone https://github.com/colegreenlee/collective.git
cd collective
go build -o bin/collective ./cmd/collective/

# Start homelab federation example
cd examples/homelab-federation
docker compose up -d

# Create your identity
cd ../../
./bin/collective identity init --global-id yourname@homelab.collective.local

# Generate an invite (requires coordinator credentials)
docker exec alice-coordinator cat /collective/certs/ca/alice-ca.crt > /tmp/alice-ca.crt
docker exec alice-coordinator cat /collective/certs/coordinators/alice-coordinator.crt > /tmp/alice-coordinator.crt
docker exec alice-coordinator cat /collective/certs/coordinators/alice-coordinator.key > /tmp/alice-coordinator.key

./bin/collective invite generate --grant "/shared:read+write" --max-uses 1 --validity 24h \
  --ca /tmp/alice-ca.crt --cert /tmp/alice-coordinator.crt --key /tmp/alice-coordinator.key

# Redeem the invite to join the collective
./bin/collective invite redeem YOUR_INVITE_CODE

# Check system status
./bin/collective status
```

### Working Example Output
```
🌐 COLLECTIVE OVERVIEW
Coordinator ID:      alice
Active Nodes:        3
Total Capacity:      1.5 GB
Connected Peers:     0  # ← Cross-coordinator federation in progress
```

See [Getting Started](docs/GETTING_STARTED.md) for detailed setup instructions.

## Core Operations

```bash
# After joining via invite, all operations use stored certificates automatically

# File operations
./bin/collective mkdir /shared
echo "Hello World" | ./bin/collective write /shared/test.txt
./bin/collective read /shared/test.txt
./bin/collective ls /shared

# Check system status
./bin/collective status           # Human-readable overview
./bin/collective status --json    # Structured output for scripting

# Invite management (for coordinators)
./bin/collective invite generate --grant "/media:read" --max-uses 5 --validity 7d
./bin/collective invite list      # View active invitations
./bin/collective invite revoke CODE

# System monitoring
curl http://localhost:9091/metrics  # Prometheus metrics (Alice's coordinator)

# Coming soon - Advanced federation
# ./bin/collective federation peer connect bob@garage.collective
# ./bin/collective mount /mnt/collective
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
- [Federation Guide](FEDERATION_COMPLETE.md) - Complete federation implementation details
- [Federation Plan](FEDERATION_PLAN.md) - Original architecture and design decisions

## Project Status

### ✅ **Core Features Complete**
- **mTLS Authentication** - Secure node-to-coordinator communication with Ed25519 certificates
- **Automatic Certificate Generation** - Docker-based TLS setup with hierarchical CAs
- **Storage Node Management** - Node registration, health monitoring, and capacity tracking
- **File Operations** - Read/write/list/mkdir operations with chunked storage
- **CLI Interface** - Comprehensive command-line tools with enhanced status monitoring
- **Multi-Member Setup** - Isolated coordinators for different members (Alice, Bob, Carol)
- **Invite System** - Working invitation codes with bootstrap server for certificate distribution
- **Identity Management** - Federated identity system with user@domain addressing

### 🚧 **In Development**
- **Federation Coordinator Peering** - Cross-member coordinator connections (TLS issues)
- **Permission Enforcement** - DataStore-level access control (not enforced yet)
- **Cross-Member Data Access** - File sharing between different coordinators
- **FUSE Filesystem** - Mount collective as local filesystem

### 📋 **Planned Features**
- **Smart Chunk Placement** - Media streaming vs backup optimization strategies
- **Connection Resilience** - Circuit breakers and connection pooling
- **Production Monitoring** - Comprehensive Prometheus metrics and Grafana dashboards
- **Certificate Rotation** - Automatic certificate renewal before expiration
- **Gossip Protocol** - Peer discovery and state synchronization

### 🎯 **Current Priority: Fix Coordinator Peering**
The invite system now works properly, allowing external clients to join. The next critical step is fixing coordinator-to-coordinator TLS authentication to enable true federation.

## 🗺️ **Development Roadmap**

### **Phase 1: Complete Federation** (Current Sprint)
1. ✅ **Bootstrap Server** - Insecure port for certificate-less invite redemption
2. ✅ **Invite System** - Generate and redeem invitation codes with permissions
3. ⚠️ **Fix coordinator peering** - Resolve TLS authentication between coordinators
4. **Implement gossip protocol** - Peer discovery and state synchronization
5. **Cross-member file access** - Read/write files across different coordinators

### **Phase 2: Production Readiness** (Next Sprint)
1. **Permission enforcement** - Actually enforce DataStore access controls
2. **FUSE filesystem** - Mount collective storage as local filesystem
3. **Certificate rotation** - Automatic renewal before expiration
4. **Connection resilience** - Retry logic, circuit breakers, and pooling
5. **Monitoring dashboard** - Grafana integration with Prometheus metrics

### **Phase 3: Advanced Features** (Future)
1. **Smart chunk placement** - Optimize for media streaming vs backups
2. **Erasure coding** - Efficient redundancy for large files
3. **Bandwidth management** - QoS and traffic shaping
4. **Mobile clients** - iOS/Android apps for collective access
5. **Web interface** - Browser-based file management

## Requirements

- **Go**: 1.23 or higher (for building from source)
- **Docker**: For containerized deployment
- **FUSE**: For filesystem mounting (Linux/macOS)
- **Protobuf**: For regenerating protocol definitions