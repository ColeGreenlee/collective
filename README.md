# Collective Storage System

A distributed storage collective system that enables small groups of trusted users to pool their storage resources. Built with a federated hub-and-spoke architecture where each member runs a coordinator that manages their storage nodes, with automatic peering, state synchronization, and a beautiful terminal-based monitoring interface.

## Architecture Overview

- **Single Binary**: One Go binary that can run in coordinator or node mode
- **Federated Design**: Each member runs one coordinator that peers with others
- **Hub-and-Spoke**: Coordinators manage multiple storage nodes
- **Automatic Mesh Formation**: Coordinators automatically form a full mesh topology with retry and exponential backoff
- **State Synchronization**: Periodic sync of node states across all peers

## Quick Start

### Prerequisites

- Go 1.23 or higher
- Protocol Buffers compiler (protoc)
- Docker and Docker Compose (for containerized deployment)

### Building

```bash
# Install dependencies
make deps

# Generate protobuf files
make proto

# Build the binary
make build
```

### Running a Three-Member Collective

#### Option 1: Local Development

```bash
# In separate terminals:

# Alice's coordinator
./bin/collective coordinator -c data/configs/alice-coordinator.json

# Alice's nodes
./bin/collective node -c data/configs/alice-node-01.json
./bin/collective node -c data/configs/alice-node-02.json

# Bob's coordinator
./bin/collective coordinator -c data/configs/bob-coordinator.json

# Bob's nodes
./bin/collective node -c data/configs/bob-node-01.json
./bin/collective node -c data/configs/bob-node-02.json

# Carol's coordinator
./bin/collective coordinator -c data/configs/carol-coordinator.json

# Carol's nodes
./bin/collective node -c data/configs/carol-node-01.json
./bin/collective node -c data/configs/carol-node-02.json
```

#### Option 2: Docker Compose

```bash
# Build and start the 3-member test collective
docker-compose up -d --build

# Check status of the collective
./collective status --coordinator alice:8001

# View logs
docker-compose logs -f

# Stop all services
docker-compose down
```

## Status Monitoring

The `status` command provides a comprehensive view of the collective's health:

```bash
# Check status of a specific coordinator
./collective status --coordinator localhost:8001

# From Docker environment
docker exec alice ./collective status --coordinator localhost:8001
```

The status display shows:
- Coordinator health and response time
- Local nodes with capacity utilization
- Connected peers and their nodes
- Remote node states from synchronized peers
- Total collective capacity and usage
- Beautiful progress bars and formatting via Lipgloss

## Configuration

Configuration can be provided via JSON files or command-line flags.

### Coordinator Configuration

```json
{
  "mode": "coordinator",
  "member_id": "alice",
  "coordinator": {
    "address": ":8001",
    "bootstrap_peers": [
      {
        "member_id": "bob",
        "address": "localhost:8002"
      }
    ],
    "data_dir": "./data/alice-coord"
  }
}
```

### Node Configuration

```json
{
  "mode": "node",
  "member_id": "alice",
  "node": {
    "node_id": "alice-node-01",
    "address": ":7001",
    "coordinator_address": "localhost:8001",
    "storage_capacity": 1073741824,
    "data_dir": "./data/alice-node-01"
  }
}
```

## Testing

```bash
# Run unit tests
make test

# Run integration tests  
make integration-test

# Quick single-member test
make quick-test
```

### FUSE Filesystem Testing

```bash
# Test directory operations
./collective coordinator --member-id alice --address :8001 &
./collective mkdir /test --coordinator localhost:8001
./collective ls / --coordinator localhost:8001
./collective mv /test /renamed --coordinator localhost:8001
./collective rm /renamed --coordinator localhost:8001

# Test mounting (Linux/Mac)
mkdir /tmp/mnt
./collective mount /tmp/mnt --coordinator localhost:8001 &
touch /tmp/mnt/testfile
echo "content" > /tmp/mnt/testfile
cat /tmp/mnt/testfile
ls -la /tmp/mnt/
umount /tmp/mnt
```

### Error Handling

| FUSE Error | Meaning | Common Causes |
|------------|---------|---------------|
| `ENOENT` | File/directory not found | Invalid path, deleted file |
| `EEXIST` | Already exists | Duplicate create operation |
| `EIO` | I/O error | Coordinator unavailable |
| `EPERM` | Permission denied | Future: Access control |

### Troubleshooting

**Mount fails on Linux:**
- Install FUSE: `sudo apt-get install fuse` (Ubuntu/Debian) or `sudo yum install fuse` (RHEL/CentOS)
- Add user to fuse group: `sudo usermod -a -G fuse $USER`

**Mount fails on macOS:**
- Install macFUSE: `brew install --cask macfuse`
- Enable kernel extension in System Preferences → Security & Privacy

**Windows mounting:**
- Currently not supported natively
- Use CLI directory operations: `mkdir`, `ls`, `rm`, `mv`
- WinFsp integration planned for future release

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

## Core Operations

### Directory Operations
```bash
# Create directories
./collective mkdir /documents --coordinator localhost:8001

# List directory contents  
./collective ls /documents --coordinator localhost:8001

# Move/rename directories
./collective mv /documents /archive --coordinator localhost:8001

# Remove directories
./collective rm /archive --coordinator localhost:8001
```

### Filesystem Mounting (Linux/Mac)
```bash
# Mount the collective storage as a filesystem
./collective mount /mnt/collective --coordinator localhost:8001

# Use standard filesystem operations
mkdir /mnt/collective/photos
echo "Hello World" > /mnt/collective/test.txt
cat /mnt/collective/test.txt
ls -la /mnt/collective/

# Unmount when done
umount /mnt/collective
```

## Features & Roadmap

| Phase | Status | Features | Timeline |
|-------|--------|----------|----------|
| **Phase 1: Core Infrastructure** | ✅ **Complete** | Coordinator peering, node management, state sync, beautiful CLI, Docker support, scale testing (50+ coordinators, 500+ nodes) | - |
| **Phase 2: Directory Structure** | ✅ **Complete** | Hierarchical filesystem, directory operations (mkdir, ls, rm, mv), path resolution, file metadata | - |
| **Phase 3: FUSE Integration** | ✅ **Complete** | Mountable filesystem, FUSE operations, file I/O, client-side caching, cross-platform support | - |
| **Phase 4: File Storage** | 🚧 **Next** | Chunk-based file storage, file read/write with persistence, content deduplication | 2-3 weeks |
| **Phase 5: Performance** | 📋 **Planned** | Variable chunk sizes, compression, parallel transfers, write coalescing, prefetching | 1-2 months |
| **Phase 6: Reliability** | 📋 **Planned** | Erasure coding, distributed locking, atomic operations, crash recovery, snapshots | 2-3 months |
| **Phase 7: Advanced** | 📋 **Future** | Multi-user auth, ACLs, quotas, tiered storage, S3 API, web UI | 3-6 months |

### Current Capabilities ✅
- **Distributed Coordination**: Full mesh peering with automatic discovery and state synchronization
- **Directory Tree**: Complete hierarchical filesystem with nested directories and path operations
- **FUSE Mounting**: Mount collective storage as a local filesystem (Linux/Mac)
- **File Operations**: Create, read, write, delete files through mounted filesystem
- **CLI Operations**: Command-line tools for directory management
- **Metadata Management**: File attributes, permissions, timestamps
- **Client Caching**: TTL-based metadata and attribute caching
- **Scale Tested**: Verified with 50+ coordinators and 500+ nodes

### Current Limitations ⚠️
- **File Content**: Files store metadata only - content not persisted to chunks yet
- **Windows FUSE**: No native mounting on Windows (WebDAV alternative planned)
- **Memory Storage**: All metadata kept in coordinator memory (persistence planned)
- **Single Coordinator**: No coordinator redundancy yet

### Next Major Milestone 🎯
**Phase 4: Chunk-Based File Storage** - Integrate the existing chunk storage system with the FUSE filesystem to provide full file persistence and retrieval across the collective.

## FUSE Filesystem Architecture

The collective storage includes a complete FUSE (Filesystem in Userspace) implementation for mounting distributed storage as a local filesystem.

### Components

1. **CollectiveFS** (`pkg/fuse/filesystem.go`): Main filesystem with directory operations, inode management, and coordinator communication
2. **CollectiveFile** (`pkg/fuse/file.go`): File-specific operations, read/write, attributes, and handles  
3. **Protocol Extensions** (`proto/coordinator.proto`): gRPC operations - `CreateFile`, `ReadFile`, `WriteFile`, `DeleteFile`
4. **Coordinator Integration** (`pkg/coordinator/coordinator.go`): File metadata storage with directory structure

### Data Flow

```
FUSE Operation → gRPC Request → Coordinator → Metadata Storage
     ↓              ↓              ↓              ↓
   Create()    → CreateFile()  → File Entry   → Parent/Child Links
   Write()     → WriteFile()   → Size/Time    → TODO: Chunk Storage  
   Read()      → ReadFile()    → Retrieve     → TODO: Chunk Retrieval
   Readdir()   → ListDirectory() → Children   → Files + Directories
```

### Metadata Storage Structure

```go
// In coordinator memory
directories map[string]*types.Directory  // Path-based directory metadata
fileEntries map[string]*types.FileEntry  // Path-based file metadata

// Directory metadata
type Directory struct {
    Path     string     // Full path: "/documents/photos"  
    Parent   string     // Parent path: "/documents"
    Children []string   // Child paths (dirs + files)
    Mode     os.FileMode // POSIX permissions
    Modified time.Time   // Last modified timestamp
    Owner    MemberID    // Owning collective member
}

// File metadata  
type FileEntry struct {
    Path     string        // Full path: "/documents/resume.pdf"
    Size     int64         // File size in bytes
    Mode     os.FileMode   // POSIX permissions  
    Modified time.Time     // Last modified timestamp
    ChunkIDs []ChunkID     // TODO: Chunk storage integration
    Owner    MemberID      // Owning collective member
}
```

### Platform Support

| Platform | FUSE Mounting | CLI Operations | Status |
|----------|---------------|----------------|--------|
| **Linux** | ✅ Full Support | ✅ All Operations | Native FUSE |
| **macOS** | ✅ Full Support | ✅ All Operations | Native FUSE |  
| **Windows** | ❌ Not Supported | ✅ All Operations | CLI Only (WinFsp planned) |

### Current Limitations & Future Integration

| Area | Current State | Limitation | Next Phase |
|------|---------------|------------|------------|
| **File Content** | Metadata Only | No persistence to chunks | Phase 4: Chunk Integration |
| **Performance** | Basic Caching | Individual gRPC calls | Phase 5: Connection pooling |
| **Reliability** | Memory Storage | Lost on restart | Phase 6: Embedded DB |
| **Scale** | Single Coordinator | No redundancy | Phase 6: Clustering |

## Scale Testing

The system has been tested at scale using Docker Compose with automated configuration generation.

### Running Large-Scale Tests

```bash
# Navigate to test-scale directory
cd test-scale

# Generate configuration for 50 coordinators with 500 nodes
python generate-compose.py 50 10

# Deploy the test cluster
docker-compose -f docker-compose-generated.yml up -d

# Check status from outside the network (via external coordinator)
../collective status --coordinator localhost:8001

# Clean up
docker-compose -f docker-compose-generated.yml down
```

### Test Configurations

- **Small**: 5 coordinators, 50 nodes (2.5GB total capacity)
- **Medium**: 20 coordinators, 200 nodes (10GB total capacity)  
- **Large**: 50 coordinators, 500 nodes (25GB total capacity)

The generator creates:
- Explicit service definitions for predictable networking
- Automatic peer bootstrapping with proper mesh formation
- An external coordinator for monitoring from host
- Resource limits to prevent system overload

## Development

### Project Structure
```
collective/
├── cmd/collective/       # CLI entry point and commands
├── pkg/
│   ├── coordinator/     # Coordinator logic and peering
│   ├── node/           # Storage node implementation
│   ├── protocol/       # gRPC services and messages
│   ├── storage/        # Chunk management
│   ├── types/          # Shared type definitions
│   └── config/         # Configuration management
├── proto/              # Protocol buffer definitions
├── data/
│   └── configs/        # JSON configuration files for testing
├── test-scale/         # Large-scale testing tools
│   └── generate-compose.py  # Docker config generator
└── docker-compose.yml  # 3-member test setup
```

### Key Principles
- Fail fast on configuration errors
- Graceful degradation during runtime
- Clear error messages with context
- No manual intervention after setup
- Respect member sovereignty