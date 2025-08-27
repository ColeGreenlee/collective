# System Architecture

## Overview

Collective is a distributed storage system that enables small groups of trusted users to pool their storage resources. It uses a federated hub-and-spoke architecture where each member runs a coordinator that manages their storage nodes.

## Core Design Principles

- **Single Binary**: One Go binary that can run in coordinator or node mode
- **Federated Design**: Each member runs one coordinator that peers with others
- **Hub-and-Spoke**: Coordinators manage multiple storage nodes
- **Automatic Mesh Formation**: Coordinators automatically form a full mesh topology
- **State Synchronization**: Periodic sync of node states across all peers
- **Member Sovereignty**: Each member controls their own resources

## Network Topology

```
Alice Coordinator (8001) ←→ Bob Coordinator (8002)
       ↓                           ↓
   Alice Nodes                 Bob Nodes
   - node-01 (7001)           - node-01 (7003)
   - node-02 (7002)           - node-02 (7004)
       
       ↕                           ↕
       
Carol Coordinator (8003) ←→ (bidirectional peering)
       ↓
   Carol Nodes
   - node-01 (7005)
   - node-02 (7006)
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

## Security Architecture

### Authentication

- **Ed25519 Certificates**: Fast, secure asymmetric cryptography
- **Per-Member CAs**: Each member has their own Certificate Authority
- **mTLS Everywhere**: All connections use mutual TLS authentication
- **Auto-Generation**: Certificates can be auto-generated for easy setup

### Trust Model

- **Federated Trust**: Members explicitly trust peer coordinators
- **Isolated Domains**: Each member's CA only works with their components
- **No Cross-Trust**: Alice's certificates won't work with Bob's coordinator

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

## Future Architecture

### Phase 7: Reliability

- Node heartbeats and health monitoring
- Automatic re-registration after failures
- Erasure coding for better storage efficiency
- Distributed locking for consistency

### Phase 8: Advanced Features

- Persistent metadata storage
- Coordinator redundancy/failover
- Cross-member chunk sharing
- S3-compatible API
- Web UI for management