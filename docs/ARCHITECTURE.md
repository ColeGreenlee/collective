# System Architecture

## Overview

Collective is a federated distributed storage system that enables small groups of trusted users to pool their storage resources. It uses a production-grade federation architecture where each member operates as an independent domain with their own Certificate Authority, participating in a gossip-based peer discovery network with granular permissions and smart chunk placement strategies.

## Core Design Principles

- **Single Binary**: One Go binary that can run in coordinator or node mode
- **Full Federation**: Mastodon-style addressing (node@domain.collective.local)
- **Hierarchical Trust**: Multi-level CA with per-member certificate authorities
- **Gossip Protocol**: Epidemic-style peer discovery with eventual consistency
- **Smart Placement**: Media, Backup, and Hybrid strategies for optimal performance
- **Member Sovereignty**: Each member controls their own resources and permissions
- **Production Ready**: Health monitoring, metrics, resilience patterns

## Network Topology

```
Federation Domain: collective.local
                    │
    ┌───────────────┼───────────────┐
    │               │               │
alice@home         bob@garage      carol@office
.collective.local  .collective.local .collective.local
    │               │               │
    CA              CA              CA    (Per-member CAs)
    │               │               │
Coordinator ←──────→ Coordinator ←→ Coordinator
    │               │               │     (Gossip Protocol)
    ├─ Node-01      ├─ Node-01      ├─ Node-01
    ├─ Node-02      ├─ Node-02      ├─ Node-02
    └─ Node-03      └─ Node-03      └─ Node-03
    
DataStores with Permissions:
/media (owner: alice) → bob:read+write, carol:read
/backup (owner: bob) → *:read+write (federation-wide)
/shared (owner: carol) → alice:read+write+share
```

## Components

### Coordinator

The coordinator is the brain of each member's storage collective:

- **Peer Management**: Maintains connections to other coordinators
- **Node Registry**: Tracks and manages local storage nodes
- **Metadata Storage**: Maintains file and directory metadata
- **State Synchronization**: Shares node states with peers
- **Client Interface**: Provides gRPC API for client operations
- **Authentication**: Manages mTLS connections with Ed25519 certificates

### Storage Node

Storage nodes provide the actual storage capacity:

- **Chunk Storage**: Stores file chunks with deduplication
- **Health Reporting**: Reports capacity and status to coordinator
- **Direct Transfers**: Handles chunk uploads/downloads from clients
- **Local Persistence**: Manages chunk storage on local filesystem

### Client

The client provides user interaction with the collective:

- **CLI Interface**: Command-line tools for all operations
- **FUSE Mount**: Filesystem mounting capability (Linux/macOS)
- **Context Management**: Multiple collective configurations
- **Authentication**: Client certificates for secure connections

## Data Flow

### File Upload

1. Client initiates upload request to coordinator
2. Coordinator allocates chunks across available nodes
3. Client uploads chunks directly to storage nodes
4. Coordinator updates metadata and replication info
5. State synchronized to peer coordinators

### File Download

1. Client requests file from coordinator
2. Coordinator provides chunk locations
3. Client retrieves chunks from storage nodes
4. Client assembles chunks into complete file

## Federation Architecture

### Gossip Protocol
- **Epidemic Dissemination**: State spreads exponentially through the network
- **Lamport Clocks**: Versioning for eventual consistency
- **Failure Detection**: Suspected/Dead states with configurable timeouts
- **Anti-Entropy**: Periodic full state synchronization

### DataStore Permissions
- **Granular Rights**: Read, Write, Delete, Share permissions
- **Wildcard Support**: `*@*.collective.local` for federation-wide access
- **Directory Inheritance**: Permissions cascade to subdirectories
- **Time-Limited Access**: Optional expiration for temporary grants

### Smart Chunk Placement
- **Media Strategy**: Low-latency placement for streaming (local nodes preferred)
- **Backup Strategy**: Cross-member diversity for maximum durability
- **Hybrid Strategy**: Balanced performance and reliability
- **Popularity-Based Replication**: Hot content gets more replicas

### Connection Resilience
- **Connection Pooling**: Reused gRPC connections with health checks
- **Circuit Breakers**: Prevent cascade failures
- **Exponential Backoff**: Smart retry logic
- **Automatic Failover**: Route around failed nodes

## Security Architecture

### Authentication

- **Ed25519 Certificates**: Fast, secure asymmetric cryptography
- **Per-Member CAs**: Each member has their own Certificate Authority
- **mTLS Everywhere**: All connections use mutual TLS authentication
- **Auto-Generation**: Certificates can be auto-generated for easy setup

### Trust Model

- **Hierarchical CA**: Root federation CA → Member CAs → Component certificates
- **Multi-CA Trust Store**: Validates certificates from multiple member CAs
- **Isolated Domains**: Each member's CA only works with their components
- **No Cross-Trust**: Alice's certificates won't work with Bob's coordinator
- **Invite System**: Pre-authenticated codes for new member onboarding

## Storage Architecture

### Chunk Management

- **Variable Sizes**: 64KB, 1MB, or 4MB chunks based on file size
- **Deduplication**: Content-addressed storage reduces redundancy
- **Parallel Operations**: Concurrent chunk transfers for performance
- **Streaming**: Large files use streaming to minimize memory usage

### Replication

- **Configurable Factor**: Default replication factor of 2
- **Cross-Node**: Chunks replicated across different nodes
- **Future**: Cross-member replication planned

## Performance Optimizations

### Memory Efficiency

- **Streaming Chunks**: 3MB buffers keep memory usage under 30MB
- **Connection Pooling**: Reuses gRPC connections to nodes
- **Lazy Loading**: Metadata loaded on-demand

### Concurrency

- **Per-File Locking**: Prevents corruption during concurrent uploads
- **Parallel Transfers**: Multiple chunks transferred simultaneously
- **Async State Sync**: Non-blocking peer synchronization

## Scalability

### Tested Limits

- **Coordinators**: 50+ coordinators in mesh network
- **Nodes**: 500+ total storage nodes
- **Performance**: 248 MB/s write, 437 MB/s read (500MB files)
- **Memory**: <30MB coordinator memory for any file size

### Bottlenecks

- **Metadata in Memory**: All metadata currently kept in RAM
- **Single Coordinator**: No coordinator redundancy yet
- **Full Mesh**: O(n²) connections between coordinators

## Monitoring & Observability

### Prometheus Metrics
- **Federation Metrics**: Connection health, gossip peers, placement operations
- **Node Metrics**: Storage usage, chunk operations, latency
- **Cluster Health**: Overall health score (0-100)
- **Per-Node Health**: Individual node health tracking

### Health Endpoints
- `/health`: Overall cluster health with score
- `/health/live`: Simple liveness check
- `/health/ready`: Readiness for traffic (health > 30)
- **Alerting Rules**: Pre-configured Prometheus alerts

## Future Enhancements

### Performance Optimizations
- **Erasure Coding**: Reduce storage overhead while maintaining reliability
- **Deduplication**: Cross-member deduplication for common content
- **Tiered Storage**: Hot/cold data separation with different strategies
- **Edge Caching**: CDN-like edge caching for popular content

### Advanced Features
- **CRDT Conflict Resolution**: Handle concurrent updates across federation
- **Geographic Awareness**: Placement decisions based on proximity
- **Bandwidth Management**: Rate limiting and QoS for federation traffic
- **S3-Compatible API**: Industry-standard object storage interface
- **Web UI**: Management dashboard for federation monitoring