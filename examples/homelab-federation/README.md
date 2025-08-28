# Homelab Federation Example

This example demonstrates a distributed storage collective with three homelab operators: Alice, Bob, and Carol. Each operates their own independent storage infrastructure with full TLS/mTLS authentication and an invitation-based joining system.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────┐
│                   COLLECTIVE FEDERATION                      │
├───────────────────┬────────────────┬────────────────────────┤
│  Alice's Homelab  │  Bob's Garage  │  Carol's Office       │
│  (home.alice)     │  (garage.bob)  │  (office.carol)       │
├───────────────────┼────────────────┼────────────────────────┤
│ 1 Coordinator     │ 1 Coordinator  │ 1 Coordinator         │
│ 3 Storage Nodes   │ 3 Storage Nodes│ 3 Storage Nodes       │
│ 1.5GB Total       │ 1.5GB Total    │ 1.5GB Total           │
└───────────────────┴────────────────┴────────────────────────┘
                    Total: 4.5GB Federated Storage
```

## Quick Start

### 1. Start the Federation

```bash
# Start all services
docker compose up -d

# Wait for services to be healthy (about 30 seconds)
docker compose ps

# View logs if needed
docker compose logs -f
```

### 2. Join the Collective as an External Client

```bash
# Build the collective CLI if not already done
cd ../../
go build -o bin/collective ./cmd/collective/

# Create your federated identity
./bin/collective identity init --global-id yourname@homelab.collective.local

# Get coordinator credentials to generate an invite
# (In production, the coordinator admin would do this)
docker exec alice-coordinator cat /collective/certs/ca/alice-ca.crt > /tmp/alice-ca.crt
docker exec alice-coordinator cat /collective/certs/coordinators/alice-coordinator.crt > /tmp/alice-coordinator.crt  
docker exec alice-coordinator cat /collective/certs/coordinators/alice-coordinator.key > /tmp/alice-coordinator.key

# Generate an invitation code
./bin/collective invite generate \
  --grant "/shared:read+write" \
  --grant "/media:read" \
  --max-uses 1 \
  --validity 24h \
  --ca /tmp/alice-ca.crt \
  --cert /tmp/alice-coordinator.crt \
  --key /tmp/alice-coordinator.key

# Redeem the invitation (use the code from above)
./bin/collective invite redeem YOUR_INVITE_CODE

# Verify you're connected
./bin/collective status
```

### 3. Work with Files

After joining via invite, all operations automatically use your stored certificates:

```bash
# Create directories
./bin/collective mkdir /shared
./bin/collective mkdir /shared/media
./bin/collective mkdir /shared/documents

# Write files
echo "Welcome to the collective!" | ./bin/collective write /shared/README.txt
echo "Important document" | ./bin/collective write /shared/documents/important.txt

# Upload larger files
cat ~/Downloads/movie.mp4 | ./bin/collective write /shared/media/movie.mp4

# Read files
./bin/collective read /shared/README.txt

# List directory contents
./bin/collective ls /shared
./bin/collective ls /shared/media
```

### 4. Monitor Storage Distribution

```bash
# Check overall status
./bin/collective status

# View JSON output for scripting
./bin/collective status --json | jq '.local_nodes[] | {id: .id, capacity: .total_capacity, used: .used_capacity}'

# Monitor metrics
curl http://localhost:9091/metrics | grep collective_  # Alice's metrics
curl http://localhost:9092/metrics | grep collective_  # Bob's metrics
curl http://localhost:9093/metrics | grep collective_  # Carol's metrics
```

## Advanced Usage

### Managing Invitations

Coordinator administrators can manage invitations:

```bash
# Generate invites with specific permissions
./bin/collective invite generate \
  --grant "/media:read" \
  --grant "/backups:write" \
  --max-uses 5 \
  --validity 7d \
  --description "Media team access" \
  --ca /tmp/alice-ca.crt \
  --cert /tmp/alice-coordinator.crt \
  --key /tmp/alice-coordinator.key

# List active invitations
./bin/collective invite list \
  --ca /tmp/alice-ca.crt \
  --cert /tmp/alice-coordinator.crt \
  --key /tmp/alice-coordinator.key

# Revoke an invitation
./bin/collective invite revoke INVITE_CODE \
  --ca /tmp/alice-ca.crt \
  --cert /tmp/alice-coordinator.crt \
  --key /tmp/alice-coordinator.key
```

### Working with Multiple Collectives

```bash
# Your identity can join multiple collectives
# Generate an invite from Bob's collective
docker exec bob-coordinator cat /collective/certs/ca/bob-ca.crt > /tmp/bob-ca.crt
docker exec bob-coordinator cat /collective/certs/coordinators/bob-coordinator.crt > /tmp/bob-coordinator.crt
docker exec bob-coordinator cat /collective/certs/coordinators/bob-coordinator.key > /tmp/bob-coordinator.key

./bin/collective invite generate \
  --grant "/garage:read+write" \
  --ca /tmp/bob-ca.crt \
  --cert /tmp/bob-coordinator.crt \
  --key /tmp/bob-coordinator.key

# Redeem Bob's invite (connects to port 8002)
./bin/collective invite redeem BOB_INVITE_CODE

# Switch between collectives
./bin/collective identity switch alice-homelab
./bin/collective identity switch bob-homelab

# List your collectives
./bin/collective identity list-collectives
```

## Understanding the Architecture

### Storage Distribution

When you write a file, it's automatically chunked and distributed across storage nodes:

```bash
# Write a file (gets split into chunks)
echo "Test content" | ./bin/collective write /shared/test.txt

# The file is stored as chunks across alice-node-01, alice-node-02, alice-node-03
# Each chunk is replicated based on the replication factor

# Check where chunks are stored
./bin/collective status --json | jq '.local_nodes[] | {id: .id, chunks: .chunk_count}'
```

### Security Model

The collective uses a hierarchical certificate authority structure:

```
Federation Root CA (future)
├── Alice CA → Signs certificates for Alice's infrastructure
│   ├── alice-coordinator certificate
│   ├── alice-node-01 certificate
│   ├── alice-node-02 certificate
│   └── alice-node-03 certificate
├── Bob CA → Signs certificates for Bob's infrastructure
└── Carol CA → Signs certificates for Carol's infrastructure
```

External clients receive certificates via the invite system:
1. Client creates identity with public/private key pair
2. Coordinator generates invite code with permissions
3. Client redeems invite via insecure bootstrap port (9001)
4. Bootstrap server validates invite and signs client certificate
5. Client uses certificate for all future secure connections

### Monitoring with Prometheus

```bash
# Access Prometheus UI
open http://localhost:9090

# Useful queries:
collective_cluster_health{member="alice"}          # Health status
collective_nodes_total{member="alice"}            # Number of nodes
collective_storage_capacity_bytes{member="alice"} # Total capacity
collective_storage_used_bytes{member="alice"}     # Used storage
collective_file_operations_total                  # Operation counts

# Check all members at once
sum by (member) (collective_nodes_total)
```

## Production Deployment

### Environment Variables

Customize the deployment with environment variables:

```yaml
environment:
  - COLLECTIVE_MEMBER_ID=alice
  - COLLECTIVE_COORDINATOR_ADDRESS=0.0.0.0:8001
  - COLLECTIVE_BOOTSTRAP_ADDRESS=0.0.0.0:9001  # For invite redemption
  - COLLECTIVE_METRICS_ADDRESS=0.0.0.0:9090
  - COLLECTIVE_LOG_LEVEL=info
  - COLLECTIVE_DATA_DIR=/data
  - COLLECTIVE_CERT_DIR=/collective/certs
```

### Scaling Storage Nodes

```bash
# Add more storage nodes by updating docker-compose.yml
# Then restart to apply changes
docker compose up -d --scale alice-node=5

# Verify new nodes registered
./bin/collective status
```

### Backup and Recovery

```bash
# Backup coordinator metadata
docker exec alice-coordinator tar -czf - /data > alice-backup.tar.gz

# Backup certificates (critical!)
docker exec alice-coordinator tar -czf - /collective/certs > alice-certs-backup.tar.gz

# Restore from backup
docker cp alice-backup.tar.gz alice-coordinator:/tmp/
docker exec alice-coordinator tar -xzf /tmp/alice-backup.tar.gz -C /
```

## Key Features

✅ **Secure by Default**: All connections use mTLS authentication
✅ **Invitation System**: Easy onboarding with permission-scoped invites
✅ **Bootstrap Server**: Certificate-less invite redemption on port 9001
✅ **Automatic Chunking**: Files split and distributed across nodes
✅ **Health Monitoring**: Built-in health checks and Prometheus metrics
✅ **Identity Management**: Federated identities with user@domain addressing

## Current Limitations

1. **Coordinator Peering**: Alice, Bob, and Carol coordinators cannot yet share data due to TLS authentication issues between coordinators
2. **Permission Enforcement**: DataStore permissions are not enforced yet - all authenticated users have full access
3. **Cross-Member Access**: Cannot access files from other coordinators (Bob/Carol) when connected to Alice

## Troubleshooting

### Health Checks

```bash
# Check if all services are healthy
docker compose ps

# Check coordinator health endpoints (these are Prometheus metrics endpoints)
curl http://localhost:9091/metrics  # Alice
curl http://localhost:9092/metrics  # Bob
curl http://localhost:9093/metrics  # Carol

# View detailed logs
docker compose logs alice-coordinator
docker compose logs bob-coordinator
docker compose logs carol-coordinator
```

### Common Issues

#### "connection refused" or "EOF" errors
- You need to join via invite first: `./bin/collective invite redeem CODE`
- The system requires TLS certificates for all operations

#### "Parent directory does not exist"
- Create parent directories first: `./bin/collective mkdir /shared`

#### Cannot connect to Bob or Carol
- Currently coordinators maintain isolated storage
- Cross-coordinator federation is still in development

#### "Invalid invite code"
- Invite codes are single-use by default
- Check expiration with `./bin/collective invite list`

### Reset Everything

```bash
# Stop and remove all containers and volumes
docker-compose down -v

# Start fresh
docker-compose up -d
```

### Network Connectivity

All services are on the same Docker network (`172.20.0.0/16`):
- Alice's homelab: `172.20.0.10-13`
- Bob's homelab: `172.20.0.20-23`
- Carol's homelab: `172.20.0.30-33`
- Prometheus: `172.20.0.100`

## Port Mapping

| Service | Internal Port | External Port | Purpose |
|---------|--------------|---------------|---------|
| Alice Coordinator | 8001 | 8001 | gRPC API |
| Alice Coordinator | 9090 | 9091 | Metrics |
| Bob Coordinator | 8001 | 8002 | gRPC API |
| Bob Coordinator | 9090 | 9092 | Metrics |
| Carol Coordinator | 8001 | 8003 | gRPC API |
| Carol Coordinator | 9090 | 9093 | Metrics |
| Prometheus | 9090 | 9090 | Web UI |

## Resource Usage

- **Storage**: Each node has 512MB capacity (9 nodes × 512MB = 4.5GB total)
- **Memory**: ~50MB per coordinator, ~30MB per storage node
- **CPU**: Minimal usage, scales with operations
- **Network**: Private Docker network with static IPs

## Next Steps

1. **Scale Testing**: Increase storage capacity and add more nodes
2. **Cross-Network**: Deploy on actual separate networks with VPN
3. **Production Setup**: Use external storage volumes and persistent data
4. **Monitoring**: Set up Grafana dashboards for Prometheus metrics
5. **Backup Strategy**: Implement automated backups using the federation

## Clean Up

```bash
# Stop all services
docker-compose down

# Remove all data (careful!)
docker-compose down -v

# Remove built images
docker-compose down --rmi all
```