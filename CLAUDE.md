# CLAUDE.md - Collective Storage System Development Guide

## Role & Self-Learning Mandate
You are a senior software engineer working on a distributed storage collective system. This is a **living document** that you must continuously update with discovered patterns, pitfalls, and improvements.

**CRITICAL**: Update this CLAUDE.md file whenever you:
- Discover better workflows or debugging techniques
- Encounter new issues or solutions
- Find more efficient development approaches
- Learn new patterns specific to this codebase

Focus on rapid iteration with built-in debugging tools over extensive upfront planning.

## Development Philosophy
1. **TodoWrite for Complex Tasks**: Break down multi-step work into trackable todos
2. **Code + Test Quickly**: Use established debugging infrastructure to iterate fast
3. **Debug with Real Data**: Trust coordinator logs, status commands, and Docker testing over theoretical analysis
4. **Fix Real Problems**: Address issues found through testing, not hypothetical edge cases
5. **Document Success**: Update this guide after proving features work

## Essential Tools & Workflow

### Core Debugging Tools (Use First)
1. **TodoWrite**: Track all multi-step tasks - use proactively for complex work
2. **Status Command**: `./bin/collective status` - uses client config context automatically
3. **JSON Debug**: `./bin/collective status --json` - structured debug data
4. **Config Context**: `./bin/collective config show-context` - view auth status and certificates
5. **Auth Info**: `./bin/collective auth info --context` - detailed certificate information
6. **Docker Logs**: `docker-compose logs -f` - real-time container debugging
7. **Background Bash**: Run processes with `&`, monitor via BashOutput tool
8. **Protocol Buffers**: Regenerate after `.proto` changes: `protoc --go_out=. --go-grpc_out=. proto/coordinator.proto`

### Standard Development Cycle
1. **TodoWrite**: Break task into trackable steps
2. **Build Clean**: `go build -o bin/collective ./cmd/collective/` (keeps repo root clean)
3. **Test Real**: Use actual coordinator/node instances, never mocks
4. **Debug with Data**: Status command and logs reveal actual issues
5. **Fix Real Problems**: Address testing failures, not theoretical issues
6. **Complete Todos**: Mark TodoWrite items done as you finish
7. **Update CLAUDE.md**: Document new patterns/solutions immediately
8. **Verify End-to-End**: Test complete workflows

### Repository Standards
**Build Location**: Always build to `bin/collective` - never leave binaries in repo root
**Clean Repo**: `git status` should show minimal untracked files
**Standard Commands**:
```bash
go build -o bin/collective ./cmd/collective/
./bin/collective status --coordinator localhost:8001
docker compose -f examples/three-member/docker-compose.yml up -d --build
```

## Authentication & Configuration Patterns

### Client Configuration System
The collective now uses a context-based configuration system similar to kubectl:

```bash
# Initialize a new context
./bin/collective init --name homelab --coordinator localhost:8001 --member alice --ca-cert ca.crt

# List contexts
./bin/collective config get-contexts

# Switch contexts
./bin/collective config use-context homelab

# View detailed context info including cert status
./bin/collective config show-context

# All commands now use the current context automatically
./bin/collective status  # No need to specify --coordinator
```

### Certificate Management
- **Auto-generation**: Docker containers auto-generate certificates with `COLLECTIVE_AUTO_TLS=true`
- **Per-member CAs**: Each member has their own CA - alice's certs won't work with bob's coordinator
- **Ed25519 keys**: Fast, secure, small key sizes (not post-quantum but excellent for current use)
- **Certificate paths**: `~/.collective/certificates/<context-name>/`
- **Expiry monitoring**: `collective auth info --context` shows expiry warnings

### Human-Friendly Sizes
Storage capacity now accepts human-friendly formats:
```bash
# Command line
./bin/collective node --capacity 500GB

# Config files
"storage_capacity": "1.5TB"  # Also supports: 100MiB, 2.5GB, etc.
```

## Common Pitfalls & Solutions

### Authentication Pitfalls
- **Missing certificates**: Run `collective init` to set up client config
- **Wrong context**: Check `collective config current-context`
- **Expired certs**: Use `collective auth info --context` to check expiry
- **Cross-member auth**: Alice's certificates won't work with Bob's coordinator (by design)
- **TLS handshake failures**: Verify CA cert matches the coordinator's CA

### Docker Development Traps
- **Binary Changes**: Always rebuild containers after Go binary changes: `docker compose -f examples/three-member/docker-compose.yml up -d --build`
- **Port Conflicts**: Check if previous containers are still running: `docker ps` and `docker compose down`
- **Log Noise**: Use `docker compose logs -f [service-name]` to filter logs
- **Network Issues**: Containers use different addresses - use service names, not localhost
- **Node Registration Loss**: After coordinator restart, nodes don't auto-register - restart nodes or implement heartbeats
- **Build Context**: Docker compose files in examples/ need `build: ../..` to access Dockerfile

### Protocol Buffer Gotchas  
- **Generated Files Location**: Files go to nested paths, move them to `pkg/protocol/`
- **Import Paths**: Regeneration can break if go_package option is wrong
- **Build Failures**: Always regenerate protobufs before building after .proto changes
- **Method Signatures**: New RPC methods need implementation in coordinator

### Development Workflow Pitfalls
- **Forgetting TodoWrite**: Complex tasks without tracking lead to incomplete work
- **Testing in Isolation**: Always test with real coordinator/node interactions
- **Ignoring Logs**: The coordinator/node logs contain crucial debugging information
- **Manual Testing Only**: Use our automated Docker setup for consistent testing
- **Creating Unnecessary Scripts**: Use the CLI directly - don't create test scripts when CLI commands work
- **Compression on Random Data**: Compression severely slows down non-compressible data - disable for testing
- **gRPC 4MB Limit**: Files >4MB require streaming implementation, not direct RPC
- **Error Formatting**: Check error handling - `%w` with nil errors causes cryptic messages

### Quick Recovery Commands
```bash
# Kill all processes and restart clean
docker-compose down && docker-compose up -d --build

# Rebuild binary to standard location and restart local testing
go build -o bin/collective cmd/collective/main.go
pkill -f collective || true
./bin/collective coordinator --member-id alice --address :8001 &

# Check what's actually running
docker ps
ps aux | grep collective
./bin/collective status --coordinator localhost:8001

# Get structured debug information
./bin/collective status --coordinator localhost:8001 --json

# Protocol buffer emergency regeneration
rm pkg/protocol/coordinator*.go
protoc --go_out=./pkg/protocol --go-grpc_out=./pkg/protocol proto/coordinator.proto
mv pkg/protocol/collective/pkg/protocol/coordinator*.go pkg/protocol/
rm -rf pkg/protocol/collective
```

## Implementation Standards

### Code Quality Requirements
- **Error Handling**: Return meaningful errors with debugging context
- **Structured Logging**: Use zap.Logger with component, operation, and ID context
- **Consistent Patterns**: Follow existing coordinator/node code patterns
- **Protocol First**: Design gRPC operations before implementing business logic
- **Incremental Success**: Get one piece fully working before moving to next

### Development Approach
- **Fail Fast**: Build and test immediately rather than perfect upfront design
- **Real Data Testing**: Use actual gRPC calls and coordinator state, never mocks
- **Observable Debugging**: Use status commands and logs instead of print statements
- **Document Discoveries**: Add new patterns, pitfalls, and solutions to this CLAUDE.md immediately

## Project Structure (Updated)
```
collective/
├── cmd/collective/       # CLI with status command and operations
│   ├── main.go         # Entry point with command registration
│   ├── auth.go         # Authentication commands (init, cert, info)
│   ├── config_cmd.go   # Config management (contexts, show-context)
│   ├── init.go         # Client initialization command
│   └── status_enhanced.go # Enhanced status with auth support
├── pkg/
│   ├── auth/           # Authentication and certificate management
│   ├── coordinator/    # Coordinator with directory + file operations  
│   ├── node/           # Storage nodes with chunk management
│   ├── protocol/       # gRPC definitions and networking
│   ├── fuse/           # FUSE filesystem implementation
│   ├── storage/        # Chunk storage and retrieval
│   ├── types/          # Shared type definitions
│   ├── config/         # Configuration management
│   └── utils/          # Utility functions
├── proto/              # Protocol buffer definitions
├── docs/               # Focused documentation
│   ├── GETTING_STARTED.md # Quick start guide
│   ├── ARCHITECTURE.md    # System design
│   ├── PERFORMANCE.md     # Benchmarks and optimization
│   ├── FUSE.md            # Filesystem documentation
│   └── *.md               # Other design docs
├── examples/           # Example deployments and configs
│   ├── single-node/    # Simple local setup
│   ├── three-member/   # Test collective with Docker
│   ├── homelab-peering/# Cross-network examples
│   └── scale-testing/  # Large-scale testing tools
├── test/               # Test files and configs
│   ├── configs/        # Test configuration files
│   ├── scripts/        # Test automation scripts
│   └── integration/    # Integration tests
├── .github/workflows/  # CI/CD pipelines
│   ├── ci.yml         # Main CI pipeline
│   └── examples.yml   # Example testing
└── docker-entrypoint.sh # Auto-TLS setup for containers
```

## Performance Metrics

### Phase 4 Baseline (3MB file test)
- **Write Speed**: 8.5 MB/s (352ms for 3MB)
- **Read Speed**: 40.5 MB/s (74ms for 3MB)
- **Chunk Size**: 1MB default
- **Replication Factor**: 2

### Phase 5 Performance (1GB file test)
- **Write Speed**: 78 MB/s (13.7s for 1GB zero-filled data)
- **Read Speed**: 320 MB/s (3.3s for 1GB) 
- **Throughput Improvement**: **9.2x faster** write than baseline
- **Chunks**: Optimized with variable sizing (256 x 4MB chunks for 1GB)
- **Features Implemented**:
  - Streaming gRPC for files >4MB
  - Parallel chunk storage/retrieval
  - Connection pooling to nodes
  - Real-time progress monitoring
  - Compression for compressible data (1GB → 1.1MB for zeros)
  - Variable chunk sizes (64KB/1MB/4MB based on file size)
  - Write coalescing buffers for small writes

### Bottlenecks Resolved
1. ✅ **gRPC 4MB Message Limit**: Fixed with streaming
2. ✅ **Sequential Chunk Operations**: Fixed with parallel goroutines
3. ✅ **No Connection Pooling**: Fixed with connection cache

### Known Performance Issues
- **Compression Bottleneck**: gzip compression on non-compressible data causes severe slowdowns (>2min for 100MB)
  - **Workaround**: Disable compression for binary/random data
  - **Future Fix**: Implement compression sampling or content-type detection
- **Chunk Decompression**: Streaming reads don't handle compressed chunks properly
  - **Issue**: Metadata doesn't track per-chunk compression state
  - **Future Fix**: Add compression flags to chunk metadata

### Critical Known Issues
⚠️ **MUST FIX BEFORE PRODUCTION**:
1. ✅ **Concurrent Upload Corruption**: FIXED - Now 100% success rate
   - Previous: 80% failure rate with parallel uploads
   - Fix applied: Per-file locking + reduced mutex scope + merge allocations
   - Testing: Verified with 10+ concurrent 5MB uploads
   
2. **Node Self-Healing**: Nodes don't auto-register after coordinator restart
   - Impact: Manual intervention required after crashes
   - Fix needed: Implement heartbeat/re-registration mechanism

## Proven Patterns & Testing

### Performance Testing
**IMPORTANT**: Always use the standardized performance testing plan documented in README.md Section "Performance Benchmarks" when running scale tests. This ensures consistent and comparable results.

```bash
# For complete performance testing procedures, see:
# README.md > Performance Benchmarks > Standardized Performance Testing Plan

# Quick validation after changes:
./bin/collective mkdir /perftest --coordinator localhost:8001
dd if=/dev/urandom of=/tmp/test_10mb.bin bs=1M count=10 2>/dev/null
time ./bin/collective client store --file /tmp/test_10mb.bin --id /perftest/10mb.bin --coordinator localhost:8001
./bin/collective client retrieve --id /perftest/10mb.bin --output /tmp/retrieved_10mb.bin --coordinator localhost:8001
cmp /tmp/test_10mb.bin /tmp/retrieved_10mb.bin && echo "✓ PASS" || echo "✗ FAIL"
rm -f /tmp/test_10mb.bin /tmp/retrieved_10mb.bin
```

### Expected Performance Baselines
- **Small files (1-10MB)**: 10-50 MB/s upload, 47-150 MB/s download
- **Medium files (50-100MB)**: 50-60 MB/s upload, 210-227 MB/s download  
- **Large files (200-500MB)**: 56-63 MB/s upload, 242-250 MB/s download
- **Data integrity**: Must be 100% for sequential operations

### Established Testing Infrastructure
```bash
# Quick single-coordinator test
./bin/collective coordinator --member-id alice --address :8001 &
./bin/collective mkdir /test --coordinator localhost:8001

# Docker multi-coordinator test  
docker compose -f examples/three-member/docker-compose.yml up -d --build
./bin/collective status --coordinator alice:8001

# Scale testing
cd examples/scale-testing && python generate-compose.py 50 10
docker compose -f docker-compose-generated.yml up -d
```

### Working Development Patterns
- **Protocol Extensions**: Add gRPC methods → regenerate protobufs → implement in coordinator
- **Directory Operations**: Path-based operations with parent-child relationship management
- **FUSE Integration**: CollectiveFS handles directories, CollectiveFile handles files
- **Error Propagation**: Coordinator errors → gRPC status → FUSE errno codes  
- **Caching Strategy**: TTL-based metadata cache (5s dirs, 1s attrs)
- **Background Testing**: Run coordinators with `&`, monitor via BashOutput
- **Docker Integration**: `docker-compose up -d --build` for reliable multi-node testing
- **Authentication Flow**: 
  1. Initialize context: `collective init --name lab --coordinator host:8001`
  2. Auto-generate certs: Docker with `COLLECTIVE_AUTO_TLS=true`
  3. Verify auth: `collective config show-context` or `collective auth info --context`
- **Client Config Usage**: Commands automatically use current context, no need for --coordinator flags
- **Certificate Debugging**: 
  - Check expiry: `collective auth info /path/to/cert.crt`
  - Verify chain: Built into `collective config show-context`
  - Test connection: Automatic in show-context command
- **Size Configuration**: Use "500GB" instead of calculating bytes (works in JSON and CLI)