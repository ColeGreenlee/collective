# CLAUDE.md - Collective Storage Development Guide

## Purpose
This guide documents critical development principles and patterns for the Collective federated storage system. Focus on actual testing, not workarounds.

## Core Development Principles

### 1. Test Reality, Not Workarounds
- **NEVER bypass security by using certificates from inside containers**
- Test as an external client would experience the system
- If the system requires certificates to get certificates, that's a bug to fix, not work around
- The federation invite system must work for truly external clients

### 2. Rapid Iteration with Real Systems
- Build and test immediately with actual coordinator/node instances
- Trust logs and status commands over theoretical analysis
- Fix real problems, not hypothetical edge cases

### 3. Security is Not Optional
- Never manually copy certificates between systems
- The invite system handles certificate distribution
- If external clients can't connect, the system is broken

## Critical Implementation Details

### Invite System Architecture
The invite system uses a dual-server pattern to solve the bootstrap problem:

1. **Main gRPC Server** (port 8001) - Requires mTLS for all operations
2. **Bootstrap Server** (port 9001) - Insecure, only handles:
   - `GetFederationCA` - Returns CA certificate for trust
   - `RequestClientCertificate` - Validates invite and issues client cert

**Key insight**: The bootstrap server signs certificates using the member's CA, not the coordinator cert. This ensures proper certificate chain validation.

### InviteManager Integration
- Invites stored in memory via `federation.InviteManager`
- Generated via `GenerateInvite` RPC with DataStore grants
- Redeemed via bootstrap server which calls `inviteManager.RedeemInvite()`
- Uses `federation.FederatedAddress` for user@domain addressing

## Essential Development Tools

### Command-Line Debugging Arsenal

```bash
# Status and Health Monitoring
./bin/collective status                    # Human-readable cluster status
./bin/collective status --json | jq '.'    # Structured data for analysis
./bin/collective config show-context       # Auth and certificate status

# Real-time Monitoring
docker compose logs -f                     # Stream all container logs
docker compose logs -f alice-coordinator   # Follow specific service
curl http://localhost:9090/metrics         # Prometheus metrics

# Interactive Testing
docker exec -it alice-coordinator sh       # Shell into container
docker exec alice-coordinator collective status  # Run commands in container

# Certificate Debugging
./bin/collective auth info /path/to/cert.crt  # Certificate details
openssl x509 -in cert.crt -text -noout        # Full certificate inspection
```

### Development Workflow Pattern

1. **Use TodoWrite for Complex Tasks**
   - Break down work into trackable steps
   - Mark items complete as you progress
   - Helps maintain focus and completeness

2. **Build-Test-Debug Cycle**
   ```bash
   # Clean build to bin/
   go build -o bin/collective ./cmd/collective/
   
   # Test with real infrastructure
   docker compose -f examples/homelab-federation/docker-compose.yml up -d
   
   # Debug with status and logs
   ./bin/collective status
   docker compose logs -f
   ```

3. **Protocol Buffer Updates**
   ```bash
   # After modifying .proto files
   protoc --go_out=./pkg/protocol --go-grpc_out=./pkg/protocol proto/*.proto
   
   # Fix import paths if needed
   mv pkg/protocol/collective/pkg/protocol/*.go pkg/protocol/
   rm -rf pkg/protocol/collective
   ```

## Debugging Strategies

### Connection Issues

```bash
# Test connectivity
./bin/collective config show-context  # Verifies cert chain and connection

# Check TLS configuration
docker exec alice-coordinator collective auth export-ca  # Get CA cert
openssl s_client -connect localhost:8001 -CAfile ca.crt  # Test TLS handshake

# Monitor connection pool
./bin/collective status --json | jq '.connections'
```

### Storage and Chunk Operations

```bash
# Track chunk distribution
./bin/collective status --json | jq '.nodes[] | {id: .id, chunks: .chunk_count}'

# Monitor storage usage
docker exec alice-coordinator df -h /data

# Test chunk operations directly
echo "test" | ./bin/collective write /test.txt
./bin/collective read /test.txt
```

### Federation and Gossip Protocol

```bash
# Check peer discovery (when gossip is active)
docker exec alice-coordinator collective federation peers list

# Monitor gossip convergence
docker compose logs -f | grep -i gossip

# Test cross-member operations
docker exec alice-coordinator collective write /shared/test.txt
docker exec bob-coordinator collective read /shared/test.txt
```

## Architecture Patterns

### Federation Design
- **Addressing**: Mastodon-style `node@domain.collective.local`
- **Trust Model**: Hierarchical CA with per-member certificate authorities
- **Discovery**: Gossip protocol with epidemic dissemination
- **Permissions**: DataStore-level with wildcard support

### Connection Management
- **Pooling**: Reuse gRPC connections with health checks
- **Resilience**: Circuit breakers and exponential backoff
- **Security**: mTLS everywhere with graceful fallback

### Storage Strategy
- **Chunking**: Variable sizes (64KB/1MB/4MB) based on file size
- **Placement**: Media (low-latency), Backup (durability), Hybrid strategies
- **Replication**: Configurable factor with cross-node distribution

## Testing Patterns

### Critical Testing Rule
**TEST FROM OUTSIDE**: Always test as an external client without access to container internals. If you need certificates from inside a container to make something work, the system is broken.

### Bootstrap Problem (SOLVED)
The invite system bootstrap problem has been solved:
- **Solution**: Dual-server architecture with bootstrap server on insecure port (9001)
- **Implementation**: `BootstrapCoordinator` handles only GetFederationCA and RequestClientCertificate
- **Security**: Bootstrap server ONLY provides certificates, no data operations
- **Key Files**: 
  - `pkg/coordinator/bootstrap_coordinator.go` - Bootstrap server implementation
  - `pkg/coordinator/coordinator.go` - Starts bootstrap server on port 9001
  - `cmd/collective/invite.go` - Auto-detects bootstrap ports (8001→9001)


## Common Pitfalls and Solutions

### Authentication Issues
- **Problem**: "x509: certificate signed by unknown authority"
- **Solution**: Ensure CA cert matches coordinator's CA, check with `show-context`

### Network Problems
- **Problem**: "connection refused" errors
- **Solution**: Verify port mapping, check `docker compose ps` for container status

### Storage Failures
- **Problem**: Chunks failing to store
- **Solution**: Check node capacity with `status --json`, verify nodes are registered

### Performance Issues
- **Problem**: Slow file transfers
- **Solution**: Check chunk size optimization, disable compression for binary data


## Code Organization Guidelines

### Package Structure
- `pkg/federation/` - All federation logic (gossip, permissions, placement)
- `pkg/coordinator/` - Core coordinator with clean separation
- `pkg/auth/` - Certificate and authentication management
- `cmd/collective/` - CLI commands, keep minimal logic here

### Testing Philosophy
- Integration tests over unit tests for distributed systems
- Use Docker Compose for realistic multi-node testing
- Test actual gRPC calls, not mocked interfaces

### Error Handling
- Return errors with context: `fmt.Errorf("failed to store chunk %s: %w", chunkID, err)`
- Log at appropriate levels with structured fields
- Surface user-friendly messages in CLI

## Performance Optimization Techniques

### Memory Management
- Stream large files instead of loading into memory
- Use buffered channels for parallel operations
- Clear maps and slices when done

### Concurrency Patterns
- Per-file locking to prevent corruption
- Parallel chunk transfers with worker pools
- Context cancellation for clean shutdown

### Network Optimization
- Connection pooling to reduce handshake overhead
- Batch operations when possible
- Use streaming RPCs for large transfers

## Repository Maintenance

### Clean Development
```bash
# Keep repository clean
git status  # Should show minimal untracked files
go mod tidy  # Clean up dependencies
go fmt ./...  # Format all code

# Build artifacts go in bin/
go build -o bin/collective ./cmd/collective/
```

### Documentation Updates
- Update this guide when discovering new patterns
- Keep examples/homelab-federation/README.md current
- Document new CLI commands immediately

### Version Control
- Commit logical units of work
- Write clear commit messages explaining "why" not "what"
- Never commit secrets or large binary files

## Quick Reference

### Development Cycle
```bash
# Build and test complete flow
go build -o bin/collective ./cmd/collective/
docker compose up -d --build
./bin/collective identity init --global-id test@homelab.collective.local

# Generate invite (get certs from container for testing)
docker exec alice-coordinator cat /collective/certs/ca/alice-ca.crt > /tmp/alice-ca.crt
docker exec alice-coordinator cat /collective/certs/coordinators/alice-coordinator.crt > /tmp/alice-coordinator.crt
docker exec alice-coordinator cat /collective/certs/coordinators/alice-coordinator.key > /tmp/alice-coordinator.key
./bin/collective invite generate --grant "/shared:read+write" --ca /tmp/alice-ca.crt --cert /tmp/alice-coordinator.crt --key /tmp/alice-coordinator.key

# Redeem and test
./bin/collective invite redeem INVITE_CODE
./bin/collective status  # Now works externally!
```

### Current Known Issues
1. **Coordinator Peering Failed**: TLS authentication issues between coordinators (alice↔bob↔carol)
2. **Permission Enforcement Missing**: DataStore permissions defined but not enforced
3. **Cross-Member Federation**: Cannot access data from other coordinators yet

## Contributing Improvements

When you discover new patterns, debugging techniques, or solutions:

1. Test the solution thoroughly
2. Document it in the appropriate section of this guide
3. Include example commands and expected output
4. Explain why the solution works, not just how

This guide should grow with the codebase, becoming more valuable over time as collective knowledge accumulates.