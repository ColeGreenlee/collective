# Collective Storage Federation Plan

## Executive Summary
Transform the Collective Storage system into a production-grade federated storage platform for media server collectives and homelab clusters. Features Mastodon-style addressing (`node@member.domain`), hierarchical certificate authority, gossip-based peer discovery, DataStore-level permissions, and smart chunk placement optimized for both high-bandwidth media serving and reliable backups. Targets 3-5 cooperating homelabs with 20TB+ storage each.

## Current State Analysis

### What We Have
1. **Identity System**
   - Each member has independent CA (alice-ca, bob-ca, carol-ca)
   - Components: Coordinator, Node, Client
   - Certificate-based authentication with Ed25519 keys
   - Member IDs are simple strings (alice, bob, carol)

2. **Networking**
   - Direct TCP connections with gRPC
   - Bootstrap-peers mechanism (implemented but blocked by cert trust)
   - Node addresses are simple ports (:9001)
   - Coordinator addresses are hostname:port

3. **Storage Model**
   - Shared filesystem across all members (simple initial approach)
   - Chunks distributed across nodes
   - Path-based file operations (/path/to/file)

### Current Problems
1. **Trust Model**: Each member's CA doesn't trust others
2. **Addressing**: No standard way to reference resources across federation
3. **Discovery**: No way to find nodes/resources in other members
4. **Identity**: No global identity system
5. **Permissions**: No granular access control for shared data
6. **Chunk Placement**: No optimization for media vs backup workloads
7. **Resilience**: No automatic recovery after failures

## Use Case: Media Server Collective

### Target Deployment
- **Scale**: 3-5 homelabs initially, expandable to 10+
- **Storage**: 20TB+ per homelab (60-100TB total capacity)
- **Workloads**: 
  - Media serving (Plex, Jellyfin via FUSE mounts)
  - Backup storage (photos, documents, archives)
  - Shared content libraries
- **Network**: Mixed connectivity (direct internet + VPN fallback)
- **Requirements**:
  - Production-grade resiliency
  - High-bandwidth local performance
  - Secure cross-homelab sharing
  - Simple new member onboarding

## Proposed Architecture

### 1. Federated Addressing System (Mastodon-style)

#### Resource Addressing Format
```
node-01@alice.home.collective      # Storage node
coord@bob.garage.collective         # Coordinator
/media/movies@carol.house.collective  # DataStore resource
alice@home.collective               # Member identity
```

#### Implementation
```go
// pkg/federation/address.go
type FederatedAddress struct {
    LocalPart  string  // node-01, coord, alice
    Domain     string  // alice.home.collective
    Resource   string  // /media/movies (optional DataStore path)
}

func ParseAddress(addr string) (*FederatedAddress, error)
func (a *FederatedAddress) String() string
func (a *FederatedAddress) IsLocal(myDomain string) bool

// Network endpoint discovery (Gossip-based)
type PeerEndpoint struct {
    Domain      string   // alice.home.collective
    DirectIPs   []string // Public IPs if exposed
    VPNIPs      []string // WireGuard/OpenVPN addresses
    LANIPs      []string // Local network addresses
    LastSeen    time.Time
    Latency     time.Duration
    Preference  int      // User-defined routing preference
}

// Gossip protocol for peer discovery
type GossipMessage struct {
    Type        GossipType // JOIN, LEAVE, UPDATE, HEARTBEAT
    Source      FederatedAddress
    Endpoints   []PeerEndpoint
    DataStores  []DataStoreInfo
    Timestamp   time.Time
    Signature   []byte
}
```

### 2. Certificate Authority Architecture

#### Hierarchical Trust Model
```
collective.local (Federation Root CA)
├── alice.collective.local (Member CA - Intermediate)
│   ├── coord@alice.collective.local (Coordinator cert)
│   ├── node-01@alice.collective.local (Node cert)
│   └── client@alice.collective.local (Client cert)
├── bob.collective.local (Member CA - Intermediate)
│   └── ... (similar structure)
└── carol.collective.local (Member CA - Intermediate)
    └── ... (similar structure)
```

#### Certificate Extensions
```go
// X.509 Extensions for federation
const (
    OID_FederationDomain = "1.3.6.1.4.1.99999.1"  // collective.local
    OID_MemberDomain     = "1.3.6.1.4.1.99999.2"  // alice.collective.local
    OID_ComponentType    = "1.3.6.1.4.1.99999.3"  // coordinator|node|client
    OID_FederatedAddress = "1.3.6.1.4.1.99999.4"  // full address
)
```

### 3. DataStore Permissions System

#### DataStore Model
```go
// pkg/federation/datastore.go
type DataStore struct {
    Path        string           // /media/movies, /backups/photos
    Owner       FederatedAddress // alice@home.collective
    Permissions []Permission
    Replicas    int              // Desired replication factor
    Strategy    PlacementStrategy
    Metadata    map[string]string
}

type Permission struct {
    Subject     FederatedAddress // bob@garage.collective or *@*.collective
    Rights      []Right          // READ, WRITE, DELETE, SHARE
    ValidUntil  *time.Time       // Optional expiry
}

type PlacementStrategy int
const (
    PlacementMedia    PlacementStrategy = iota // Optimize for streaming
    PlacementBackup                            // Optimize for durability
    PlacementHybrid                           // Balance both
)

// Smart chunk placement based on workload
type ChunkPlacementPolicy struct {
    DataStore   string
    Strategy    PlacementStrategy
    LocalFirst  bool     // Prefer local nodes for media
    MinReplicas int      // Minimum replicas across members
    MaxReplicas int      // Maximum for popular content
    Constraints []string // rack-aware, member-diverse
}
```

#### Permission Examples
```json
{
  "datastores": [
    {
      "path": "/media/movies",
      "owner": "alice@home.collective",
      "permissions": [
        {"subject": "*@*.collective", "rights": ["READ"]},
        {"subject": "bob@garage.collective", "rights": ["READ", "WRITE"]}
      ],
      "strategy": "media",
      "replicas": 2
    },
    {
      "path": "/backups/family-photos",
      "owner": "carol@house.collective",
      "permissions": [
        {"subject": "alice@home.collective", "rights": ["READ", "WRITE"]},
        {"subject": "bob@garage.collective", "rights": ["READ"]}
      ],
      "strategy": "backup",
      "replicas": 3
    }
  ]
}
```

### 4. Identity & Authentication

#### Identity Structure
```go
// pkg/federation/identity.go
type FederatedIdentity struct {
    Address       FederatedAddress
    ComponentType ComponentType
    Certificate   *x509.Certificate
    Capabilities  []Capability  // read, write, admin, peer
    MemberSince   time.Time
}

type Capability string
const (
    CapabilityRead   Capability = "read"
    CapabilityWrite  Capability = "write"
    CapabilityAdmin  Capability = "admin"
    CapabilityPeer   Capability = "peer"  // Can establish peer connections
)
```

#### Authentication Flow
1. **Local Operations**: Use member CA directly
2. **Federation Operations**: Validate against federation root CA
3. **Peer Discovery**: Exchange member CA certificates on first contact
4. **Trust Establishment**: Verify federation membership via root CA

### 5. Gossip Protocol & Peer Discovery

#### Gossip Implementation
```go
// pkg/federation/gossip.go
type GossipService struct {
    localDomain   string
    peers         map[FederatedAddress]*PeerState
    dataStores    map[string]*DataStore
    gossipPeriod  time.Duration // Default: 30s
    fanout        int           // Default: 3
}

type PeerState struct {
    Address     FederatedAddress
    Endpoints   []PeerEndpoint
    LastSeen    time.Time
    Version     uint64 // Lamport clock
    DataStores  []string
    Status      PeerStatus
}

// Production gossip features
func (g *GossipService) Start()
func (g *GossipService) AddPeer(addr FederatedAddress)
func (g *GossipService) HandleMessage(msg GossipMessage)
func (g *GossipService) Broadcast(update interface{})
func (g *GossipService) GetHealthyPeers() []PeerState
```

#### Network Resilience
```go
// Automatic endpoint selection with fallback
func (g *GossipService) ConnectToPeer(peer FederatedAddress) (*grpc.ClientConn, error) {
    endpoints := g.getEndpointsForPeer(peer)
    
    // Try in order of preference: LAN -> Direct -> VPN
    for _, ep := range endpoints {
        if conn, err := g.tryConnect(ep); err == nil {
            g.updateLatency(peer, ep)
            return conn, nil
        }
    }
    return nil, ErrNoValidEndpoint
}
```

### 6. Connection Management

#### Connection Types
```go
// pkg/federation/connections.go
type ConnectionType int
const (
    ConnectionLocal      ConnectionType = iota  // Within same member
    ConnectionPeer                              // Between coordinators
    ConnectionFederated                         // Cross-member operations
)

type ConnectionManager struct {
    localDomain   string
    connections   map[FederatedAddress]*grpc.ClientConn
    trustStore    *TrustStore
}
```

#### Smart Routing
```go
// Route requests based on address
func (cm *ConnectionManager) GetConnection(addr FederatedAddress) (*grpc.ClientConn, error) {
    if addr.IsLocal(cm.localDomain) {
        return cm.getLocalConnection(addr)
    }
    return cm.getFederatedConnection(addr)
}
```

### 7. Invite System & Member Onboarding

#### Pre-auth Invite Codes
```go
// pkg/federation/invite.go
type InviteCode struct {
    Code        string           // Random 16-char code
    Inviter     FederatedAddress // alice@home.collective
    Permissions []DataStoreGrant // What they'll have access to
    ExpiresAt   time.Time
    MaxUses     int
    Used        int
}

type DataStoreGrant struct {
    Path   string
    Rights []Right
}

// Generate invite for new member
func (c *Coordinator) GenerateInvite(req InviteRequest) (*InviteCode, error)

// Redeem invite during setup
func (c *Coordinator) RedeemInvite(code string, newMember FederatedAddress) error
```

#### Onboarding Flow
1. Existing member generates invite code with specific DataStore access
2. New member runs: `collective federation join --invite-code XXXX-XXXX-XXXX`
3. Automatic certificate exchange and trust establishment
4. Gossip protocol shares new member with federation
5. DataStore permissions automatically applied

### 8. Production Monitoring & Health

#### Health Metrics
```go
type FederationHealth struct {
    LocalStatus    ComponentStatus
    PeerHealth     map[FederatedAddress]PeerStatus
    DataStoreStats map[string]DataStoreMetrics
    NetworkLatency map[FederatedAddress]time.Duration
    ChunkHealth    ChunkDistributionHealth
}

type DataStoreMetrics struct {
    Size           int64
    ChunkCount     int
    ReplicationMet bool
    AccessPatterns AccessStats
    LastBackup     time.Time
}
```

### 9. Implementation Tasks (Ordered for Independent Development)

## Task Execution Order
Execute tasks in this sequence: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10

---

## Task 1: Federation Address Package
**Scope**: Create the federation addressing system with Mastodon-style format parsing and validation.

**Files to Create/Modify**:
- Create `pkg/federation/address.go`
- Create `pkg/federation/address_test.go`
- Create `pkg/federation/doc.go`

**Implementation Requirements**:
```go
type FederatedAddress struct {
    LocalPart  string  // node-01, coord, alice
    Domain     string  // alice.collective.local
    Resource   string  // /files/data.txt (optional)
}
```
- Parse addresses like `node-01@alice.collective.local`
- Validate domain format (must have at least one dot)
- Support resource paths `/path/to/file@domain`
- Implement String() for canonical representation

**Test Cases**:
1. Parse valid node address: `node-01@alice.collective.local`
2. Parse coordinator address: `coord@bob.collective.local`
3. Parse resource address: `/files/data.txt@carol.collective.local`
4. Reject invalid formats: `alice`, `@domain`, `user@`
5. Test IsLocal() method for domain matching

**Validation**:
```bash
go test ./pkg/federation -v -run TestAddress
# All tests should pass
# Should handle at least 10 different address formats
```

---

## Task 2: Federation CA Generator
**Scope**: Create tooling to generate a federation root CA and sign member intermediate CAs.

**Files to Create/Modify**:
- Create `cmd/collective/federation_ca.go`
- Modify `pkg/auth/cert_manager.go` (add intermediate CA support)
- Create `scripts/generate_federation_ca.sh`

**Implementation Requirements**:
- Generate ED25519 root CA with 5-year validity
- Generate intermediate CA CSRs for members
- Sign intermediate CAs with root CA
- Add federation domain extension (OID 1.3.6.1.4.1.99999.1)
- Save certificates in PEM format

**Test Cases**:
1. Generate root CA for `collective.local`
2. Generate and sign intermediate for `alice.collective.local`
3. Verify certificate chain validation
4. Verify federation domain in extensions
5. Test certificate expiry dates (root > intermediate > leaf)

**Validation**:
```bash
./bin/collective federation ca init --domain collective.local
./bin/collective federation ca sign-member --member alice.collective.local
openssl verify -CAfile root-ca.crt -untrusted alice-ca.crt alice-cert.crt
# Should output: alice-cert.crt: OK
```

---

## Task 3: Multi-CA Trust Store
**Scope**: Implement a trust store that can validate certificates against multiple CAs.

**Files to Create/Modify**:
- Create `pkg/federation/trust_store.go`
- Create `pkg/federation/trust_store_test.go`
- Modify `pkg/auth/tls_config.go` (use trust store)

**Implementation Requirements**:
- Load and manage multiple CA certificates
- Support federation root + all member CAs
- Validate certificate chains with proper hierarchy
- Cache validated certificates for performance
- Support CA rotation (add/remove CAs)

**Test Cases**:
1. Load federation root + 3 member CAs
2. Validate alice cert against alice CA
3. Validate bob cert against root CA (via intermediate)
4. Reject expired certificates
5. Reject certificates from unknown CAs
6. Test CA removal and re-validation

**Validation**:
```bash
go test ./pkg/federation -v -run TestTrustStore
# Create test scenario with 3 member CAs
# Validate cross-member certificate acceptance
```

---

## Task 4: Gossip Protocol Implementation
**Scope**: Implement gossip protocol for peer discovery and state sharing.

**Files to Create/Modify**:
- Create `pkg/federation/gossip.go`
- Create `pkg/federation/gossip_test.go`
- Modify `pkg/coordinator/coordinator.go` (integrate gossip)
- Create `pkg/federation/gossip_messages.proto`

**Implementation Requirements**:
- Implement epidemic-style gossip with configurable fanout
- Support JOIN, LEAVE, UPDATE, HEARTBEAT message types
- Maintain peer state with Lamport clocks
- Auto-discover peers through gossip
- Handle network partitions gracefully
- Implement anti-entropy for consistency

**Test Cases**:
1. Gossip message propagation to all peers
2. New member discovery through gossip
3. Handle network partition and recovery
4. Verify anti-entropy convergence
5. Test with 10+ peers for scalability

**Validation**:
```bash
go test ./pkg/federation -v -run TestGossip
# Start 3 coordinators and verify peer discovery
# Check logs for gossip message exchange
```

---

## Task 5: DataStore Permissions System
**Scope**: Implement DataStore-based access control with granular permissions.

**Files to Create/Modify**:
- Create `pkg/federation/datastore.go`
- Create `pkg/federation/permissions.go`
- Create `pkg/federation/datastore_test.go`
- Modify `pkg/coordinator/coordinator.go` (enforce permissions)

**Implementation Requirements**:
```go
type DataStore struct {
    Path        string
    Owner       FederatedAddress
    Permissions []Permission
    Strategy    PlacementStrategy
}
```
- Enforce permissions on all file operations
- Support wildcard permissions (*@*.collective)
- Implement permission inheritance for subdirectories
- Cache permission checks for performance
- Support time-limited permissions

**Test Cases**:
1. Create DataStore with owner permissions
2. Grant read access to specific member
3. Verify write access denied without permission
4. Test wildcard permissions
5. Test permission expiry

**Validation**:
```bash
./bin/collective datastore create /media/movies --owner alice@home.collective
./bin/collective datastore grant /media/movies read bob@garage.collective
# Verify bob can read but not write
```

---

## Task 6: Smart Chunk Placement
**Scope**: Implement intelligent chunk placement based on workload type.

**Files to Create/Modify**:
- Create `pkg/federation/placement.go`
- Create `pkg/federation/placement_strategy.go`
- Modify `pkg/coordinator/coordinator_storage.go`
- Create `pkg/federation/placement_test.go`
**Implementation Requirements**:
- Media strategy: Local-first, high-bandwidth nodes preferred
- Backup strategy: Cross-member diversity, 3+ replicas
- Hybrid strategy: Balance performance and durability
- Consider node capacity and current load
- Implement popularity-based replication (hot content)

**Test Cases**:
1. Place media chunks with local-first strategy
2. Place backup chunks across 3+ members
3. Verify load balancing across nodes
4. Test popularity-based replication
5. Handle node failure and re-placement

**Validation**:
```bash
./bin/collective datastore create /media --strategy media
./bin/collective datastore create /backups --strategy backup
# Upload files and verify chunk placement
./bin/collective debug chunks --file /media/movie.mp4
# Should show local node preference for media
```

---

## Task 7: Invite System Implementation
**Scope**: Create invite-based member onboarding with pre-authorized access.

**Files to Create/Modify**:
- Create `pkg/federation/invite.go`
- Create `cmd/collective/federation_invite.go`
- Modify `pkg/coordinator/coordinator.go` (invite handling)
- Create `pkg/federation/invite_test.go`

**Implementation Requirements**:
- Generate secure invite codes (16 chars, crypto/rand)
- Include DataStore permissions in invite
- Support expiry and max-use limits
- Store invites in coordinator state
- Exchange certificates on redemption
- Auto-apply permissions after join

**Test Cases**:
1. Generate invite with specific permissions
2. Redeem invite from new member
3. Verify permissions auto-applied
4. Test invite expiry
5. Test max-use limits

**Validation**:
```bash
# Alice generates invite for Bob
./bin/collective federation invite create --datastores /media:read,/backups:write
# Output: Invite code: XXXX-XXXX-XXXX-XXXX

# Bob joins federation
./bin/collective federation join --invite XXXX-XXXX-XXXX-XXXX --domain bob.garage.collective
# Should establish trust and apply permissions
```

---

## Task 8: Network Resilience & Failover
**Scope**: Implement automatic failover between network paths (Direct/VPN/LAN).

**Files to Create/Modify**:
- Create `pkg/federation/network_resilience.go`
- Create `pkg/federation/endpoint_manager.go`
- Modify `pkg/coordinator/coordinator.go` (use resilient connections)
- Create `pkg/federation/network_test.go`

**Implementation Requirements**:
- Try connection paths in order: LAN → Direct → VPN
- Cache successful endpoints with TTL
- Monitor endpoint health and latency
- Automatic failover on connection loss
- Support hybrid networking (some direct, some VPN)
- Handle asymmetric routing gracefully

**Test Cases**:
1. Connect via LAN when available
2. Fallback to VPN on direct failure
3. Switch back to better path when available
4. Handle multiple simultaneous failures
5. Test with mixed connectivity scenarios

**Validation**:
```bash
# Simulate network failures
./bin/collective test network --fail-direct --member bob@garage.collective
# Should automatically failover to VPN
./bin/collective status --show-connections
# Should display active connection paths
```

---

## Task 9: Production Health Monitoring
**Scope**: Implement comprehensive health monitoring for production deployments.

**Files to Create/Modify**:
- Create `pkg/federation/health.go`
- Create `pkg/federation/metrics.go`
- Create `cmd/collective/health.go`
- Modify `pkg/coordinator/coordinator.go` (expose metrics)

**Implementation Requirements**:
- Monitor coordinator, node, and peer health
- Track DataStore replication status
- Measure network latency between members
- Monitor chunk distribution and availability
- Alert on degraded performance or failures
- Export Prometheus metrics

**Test Cases**:
1. Detect node failure within 30s
2. Alert on replication below threshold
3. Track 99th percentile latencies
4. Monitor DataStore access patterns
5. Test with simulated failures

**Validation**:
```bash
./bin/collective health check --verbose
# Should show:
# - All component statuses
# - Replication health per DataStore
# - Network latencies
# - Chunk distribution stats

# Prometheus endpoint
curl localhost:9090/metrics | grep collective_
# Should show all federation metrics
```

---

## Task 10: FUSE Integration for Media Servers
**Scope**: Optimize FUSE filesystem for high-bandwidth media server workloads.

**Files to Create/Modify**:
- Modify `pkg/fuse/filesystem.go` (add caching strategies)
- Create `pkg/fuse/media_optimization.go`
- Modify `pkg/fuse/file.go` (implement read-ahead)
- Create `pkg/fuse/cache_manager.go`

**Implementation Requirements**:
- Implement aggressive read-ahead for sequential access
- Cache hot content locally (popular media files)
- Support partial file caching for large files
- Optimize for Plex/Jellyfin access patterns
- Background prefetch based on access patterns
- Support direct streaming from nodes

**Test Cases**:
1. Stream 4K video without buffering
2. Handle multiple concurrent streams
3. Cache popular content automatically
4. Verify read-ahead performance
5. Test with Plex/Jellyfin directly

**Validation**:
```bash
# Mount federation storage
./bin/collective mount /mnt/collective

# Test streaming performance
dd if=/mnt/collective/media/movie.mkv of=/dev/null bs=4M status=progress
# Should achieve 100+ MB/s for cached content

# Run Plex on mounted storage
docker run -v /mnt/collective/media:/data plexinc/pms-docker
# Should handle multiple streams without issues
```

---

## Implementation Order Rationale

The tasks are ordered to build production capabilities incrementally:

1. **Federation Address Package**: Foundation for all federation features - establishes the addressing system
2. **Federation CA Generator**: Creates the trust hierarchy needed for secure multi-member operations
3. **Multi-CA Trust Store**: Enables certificate validation across federation members
4. **Gossip Protocol**: Provides peer discovery and state sharing for production resilience
5. **DataStore Permissions**: Implements granular access control required for shared content
6. **Smart Chunk Placement**: Optimizes storage for media streaming vs backup workloads
7. **Invite System**: Enables easy onboarding of new federation members
8. **Network Resilience**: Provides automatic failover for production reliability
9. **Health Monitoring**: Essential for operating 20TB+ production deployments
10. **FUSE Optimization**: Delivers the high-bandwidth performance needed for media servers

Each task is independently valuable and testable, allowing parallel development by sub-agents while building toward a complete production-grade federation system.

### Production Configuration

#### Coordinator Configuration (Media Server Collective)
```json
{
  "federation": {
    "domain": "media.collective",
    "member_domain": "alice.media.collective",
    "root_ca": "/collective/certs/federation/root-ca.crt",
    "enable_gossip": true,
    "gossip_interval": "30s",
    "trusted_domains": ["bob.media.collective", "carol.media.collective"]
  },
  "datastores": [
    {
      "path": "/media/movies",
      "strategy": "media",
      "replicas": 2,
      "cache_popular": true
    },
    {
      "path": "/media/shows", 
      "strategy": "media",
      "replicas": 2,
      "cache_popular": true
    },
    {
      "path": "/backups/photos",
      "strategy": "backup",
      "replicas": 3,
      "cross_member": true
    }
  ],
  "network": {
    "endpoints": [
      {"type": "direct", "address": "alice.example.com:8001"},
      {"type": "vpn", "address": "10.0.0.1:8001"},
      {"type": "lan", "address": "192.168.1.100:8001"}
    ],
    "prefer_local": true,
    "fallback_enabled": true
  },
  "coordinator": {
    "address": "coord@alice.media.collective",
    "bootstrap_peers": [
      "coord@bob.media.collective",
      "coord@carol.media.collective"
    ],
    "storage_capacity": "25TB"
  }
}
```

#### Node Configuration (20TB+ Storage Node)
```json
{
  "node": {
    "address": "node-01@alice.media.collective",
    "coordinator": "coord@alice.media.collective",
    "storage_capacity": "20TB",
    "storage_path": "/mnt/storage",
    "labels": {
      "type": "spinning_disk",
      "bandwidth": "10gbps",
      "location": "homelab-rack-1"
    }
  },
  "performance": {
    "parallel_uploads": 10,
    "parallel_downloads": 20,
    "chunk_cache_size": "10GB",
    "enable_compression": false
  }
}
```

### 7. Protocol Updates

#### Enhanced PeerConnect RPC
```protobuf
message PeerConnectRequest {
  string federated_address = 1;  // coord@alice.collective.local
  string federation_domain = 2;   // collective.local
  bytes member_ca_cert = 3;       // For trust establishment
  repeated NodeInfo nodes = 4;
}
```

#### Node Registration
```protobuf
message RegisterNodeRequest {
  string federated_address = 1;  // node-01@alice.collective.local
  int64 capacity = 2;
  map<string, string> labels = 3;  // rack, datacenter, region
}
```

### 8. Security Considerations

#### Trust Boundaries
1. **Intra-member**: Full trust (same member CA)
2. **Inter-member**: Federated trust (via root CA)
3. **External**: No trust (requires explicit peering)

#### Certificate Validation Levels
```go
const (
    ValidationStrict    // Full chain to root CA
    ValidationFederated // Accept any federation member
    ValidationPeer      // Accept configured peers only
    ValidationLocal     // Same member only
)
```

### 9. Migration Strategy

#### Step 1: Add Federation Layer (Non-breaking)
- Add federation package alongside existing code
- Generate federation root CA in Docker setup
- Update certificates to include federation extensions

#### Step 2: Dual-mode Operation
- Support both old (alice) and new (alice@collective.local) addresses
- Translate between formats at boundaries
- Log deprecation warnings for old format

#### Step 3: Full Migration
- Update all internal references to federated addresses
- Remove backward compatibility code
- Enforce federation-only mode

### Testing Strategy for Production

#### Media Server Collective Test Scenarios
1. **Three-Member Media Federation**: 
   - Alice (25TB): Primary media library
   - Bob (20TB): Mirror and backups
   - Carol (30TB): Archive and overflow
   - Test 4K streaming across members
   - Verify automatic failover during outages

2. **High-Load Media Serving**:
   - 10+ concurrent Plex/Jellyfin streams
   - Mixed 4K/1080p content
   - Measure buffering and performance
   - Test cache effectiveness

3. **Network Resilience Testing**:
   - Simulate ISP outage (fallback to VPN)
   - Test asymmetric routing scenarios
   - Verify gossip convergence after partition
   - Measure failover time (<30s target)

4. **Backup Workload Testing**:
   - 1TB+ photo backup across 3 members
   - Verify cross-member replication
   - Test recovery from member loss
   - Validate 3-2-1 backup strategy

5. **New Member Onboarding**:
   - Generate invite with limited permissions
   - Join new 15TB member to federation
   - Verify automatic trust establishment
   - Test DataStore access restrictions

#### Production Test Infrastructure
```yaml
# docker-compose.production-test.yml
services:
  alice-coordinator:
    environment:
      FEDERATION_DOMAIN: media.collective
      MEMBER_DOMAIN: alice.media.collective
      STORAGE_CAPACITY: 25TB
      DATASTORE_MEDIA: /media
      DATASTORE_BACKUP: /backups
      GOSSIP_ENABLED: true
      NETWORK_ENDPOINTS: "direct:alice.example.com:8001,vpn:10.0.0.1:8001"
    volumes:
      - /mnt/storage/alice:/storage
      - ./configs/alice:/configs
    deploy:
      resources:
        limits:
          memory: 4G
        
  alice-node-01:
    environment:
      NODE_ADDRESS: node-01@alice.media.collective
      STORAGE_PATH: /storage
      STORAGE_CAPACITY: 20TB
      CACHE_SIZE: 10GB
    volumes:
      - /mnt/disk1:/storage
```

## Production Benefits

### For Media Server Collectives
1. **Unified Media Library**: Access all content across homelabs via single mount point
2. **Automatic Redundancy**: Media replicated based on popularity and importance
3. **Smart Streaming**: Local caching and prefetch for smooth 4K playback
4. **Resilient Operations**: Automatic failover keeps Plex/Jellyfin running during outages
5. **Easy Expansion**: Add new members with invite codes and automatic permissions

### For Backup & Archive
1. **3-2-1 Strategy**: Automatic cross-member replication for critical data
2. **Tiered Storage**: Hot data on fast nodes, cold archives on large capacity nodes
3. **Disaster Recovery**: Survive complete member loss with cross-member replicas
4. **Versioning Support**: Keep multiple versions across federation
5. **Compliance Ready**: Audit trails and access logs for all operations

### Technical Advantages
1. **Production-Grade**: Designed for 60-100TB deployments from day one
2. **Network Agnostic**: Works with any combination of direct, VPN, or LAN connections
3. **Zero-Trust Security**: mTLS everywhere with hierarchical CA structure
4. **Observable**: Comprehensive metrics and health monitoring
5. **Maintainable**: Clean architecture with independent, testable components

## Getting Started

### Quick Start (3-Member Media Collective)
```bash
# Member 1: Initialize federation
./bin/collective federation init --domain media.collective --member alice
./bin/collective coordinator --federation --storage 25TB

# Member 2: Join with invite
./bin/collective federation join --invite XXXX-XXXX --member bob
./bin/collective coordinator --federation --storage 20TB

# Member 3: Join with invite  
./bin/collective federation join --invite YYYY-YYYY --member carol
./bin/collective coordinator --federation --storage 30TB

# Mount and use with Plex
./bin/collective mount /mnt/collective
docker run -v /mnt/collective/media:/data plexinc/pms-docker
```

### Next Steps
1. Implement Task 1-3 (Foundation) for basic federation
2. Add Task 4-6 (Production Features) for resilience and performance
3. Deploy Task 7-10 (Polish) for complete production system
4. Begin migration of existing homelabs to federation
5. Monitor and iterate based on production usage

## Success Metrics

- **Performance**: 100+ MB/s streaming to media servers
- **Reliability**: 99.9% uptime for DataStore access
- **Scalability**: Support 10+ members with 200TB+ total capacity
- **Usability**: New member onboarding in <10 minutes
- **Efficiency**: <5% storage overhead for redundancy