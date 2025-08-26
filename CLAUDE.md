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
2. **Status Command**: `./bin/collective.exe status --coordinator localhost:8001` - beautiful cluster state UI
3. **JSON Debug**: `./bin/collective.exe status --coordinator localhost:8001 --json` - structured debug data
4. **Docker Logs**: `docker-compose logs -f` - real-time container debugging
5. **Background Bash**: Run processes with `&`, monitor via BashOutput tool
6. **Protocol Buffers**: Regenerate after `.proto` changes: `protoc --go_out=. --go-grpc_out=. proto/coordinator.proto`

### Standard Development Cycle
1. **TodoWrite**: Break task into trackable steps
2. **Build Clean**: `go build -o bin/collective.exe cmd/collective/main.go` (keeps repo root clean)
3. **Test Real**: Use actual coordinator/node instances, never mocks
4. **Debug with Data**: Status command and logs reveal actual issues
5. **Fix Real Problems**: Address testing failures, not theoretical issues
6. **Complete Todos**: Mark TodoWrite items done as you finish
7. **Update CLAUDE.md**: Document new patterns/solutions immediately
8. **Verify End-to-End**: Test complete workflows

### Repository Standards
**Build Location**: Always build to `bin/collective.exe` - never leave binaries in repo root
**Clean Repo**: `git status` should show minimal untracked files
**Standard Commands**:
```bash
go build -o bin/collective.exe cmd/collective/main.go
./bin/collective.exe status --coordinator localhost:8001
docker-compose up -d --build
```

## Common Pitfalls & Solutions

### Docker Development Traps
- **Binary Changes**: Always rebuild containers after Go binary changes: `docker-compose up -d --build`
- **Port Conflicts**: Check if previous containers are still running: `docker ps` and `docker-compose down`
- **Log Noise**: Use `docker-compose logs -f [service-name]` to filter logs
- **Network Issues**: Containers use different addresses - use service names, not localhost

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

### Quick Recovery Commands
```bash
# Kill all processes and restart clean
docker-compose down && docker-compose up -d --build

# Rebuild binary to standard location and restart local testing
go build -o bin/collective.exe cmd/collective/main.go
pkill -f collective || true
./bin/collective.exe coordinator --member-id alice --address :8001 &

# Check what's actually running
docker ps
ps aux | grep collective
./bin/collective.exe status --coordinator localhost:8001

# Get structured debug information
./bin/collective.exe status --coordinator localhost:8001 --json

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

## Project Structure
```
collective/
├── cmd/collective/       # CLI with status command and operations
├── pkg/
│   ├── coordinator/     # Coordinator with directory + file operations  
│   ├── node/           # Storage nodes with chunk management
│   ├── protocol/       # gRPC definitions and networking
│   ├── fuse/           # FUSE filesystem implementation
│   ├── storage/        # Chunk storage and retrieval
│   ├── types/          # Shared type definitions
│   └── config/         # Configuration management
├── proto/              # Protocol buffer definitions
├── data/configs/       # JSON configuration files for testing
├── docker-compose.yml  # Multi-node test environment
└── test-scale/         # Large-scale testing tools
```

## Proven Patterns & Testing

### Established Testing Infrastructure
```bash
# Quick single-coordinator test
./bin/collective.exe coordinator --member-id alice --address :8001 &
./bin/collective.exe mkdir /test --coordinator localhost:8001

# Docker multi-coordinator test  
docker-compose up -d --build
./bin/collective.exe status --coordinator alice:8001

# Scale testing
cd test-scale && python generate-compose.py 50 10
docker-compose -f docker-compose-generated.yml up -d
```

### Working Development Patterns
- **Protocol Extensions**: Add gRPC methods → regenerate protobufs → implement in coordinator
- **Directory Operations**: Path-based operations with parent-child relationship management
- **FUSE Integration**: CollectiveFS handles directories, CollectiveFile handles files
- **Error Propagation**: Coordinator errors → gRPC status → FUSE errno codes  
- **Caching Strategy**: TTL-based metadata cache (5s dirs, 1s attrs)
- **Background Testing**: Run coordinators with `&`, monitor via BashOutput
- **Docker Integration**: `docker-compose up -d --build` for reliable multi-node testing