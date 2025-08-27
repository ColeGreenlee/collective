# Homelab Peering Setup

This example demonstrates how to set up cross-network peering between two home networks, allowing trusted groups to share storage across their homelabs.

## Network Requirements

1. **Port Forwarding**: Each homelab needs to forward port 8001 to their coordinator container
2. **Dynamic DNS**: Set up DDNS (e.g., alice.homelab.com, bob.homelab.com) or use static IPs
3. **Firewall Rules**: Allow incoming connections on port 8001 from trusted peer IPs

## Deployment Steps

### On Alice's Network

```bash
# 1. Set up port forwarding on router: External 8001 → Internal Docker Host:8001
# 2. Configure DDNS or note static IP

# 3. Deploy the collective
cd alice/
docker-compose up -d

# 4. Export CA certificate for Bob
docker exec alice-coordinator cat /collective/certs/ca/alice-ca.crt > alice-ca.crt
# Send alice-ca.crt to Bob securely (encrypted email, Signal, etc.)
```

### On Bob's Network

```bash
# 1. Set up port forwarding on router: External 8001 → Internal Docker Host:8001
# 2. Configure DDNS or note static IP

# 3. Deploy the collective
cd bob/
docker-compose up -d

# 4. Export CA certificate for Alice
docker exec bob-coordinator cat /collective/certs/ca/bob-ca.crt > bob-ca.crt
# Send bob-ca.crt to Alice securely
```

## Client Configuration

```bash
# On Alice's machine - connect to both collectives
collective init \
  --name alice-local \
  --coordinator localhost:8001 \
  --member alice \
  --ca-cert alice-ca.crt

collective init \
  --name bob-remote \
  --coordinator bob.homelab.com:8001 \
  --member bob \
  --ca-cert bob-ca.crt

# List available contexts
collective config get-contexts

# Switch between collectives
collective config use-context bob-remote
collective status

# Mount the collective filesystem
collective mount /mnt/collective --coordinator localhost:8001
```

## Security Considerations

1. **Certificate Exchange**: Exchange CA certificates through secure channels
2. **Firewall Restrictions**: Only allow port 8001 from known peer IPs
3. **VPN Alternative**: Consider using WireGuard/Tailscale for secure peering without port forwarding
4. **Authentication**: Each member has their own CA - certificates from one member won't work with another's coordinator

## Network Topology

```
Alice Homelab                    Internet                    Bob Homelab
    |                               |                            |
Alice Coordinator (8001) ←-------→  |  ←-------→ Bob Coordinator (8001)
    ↓                                                            ↓
Alice Nodes                                                 Bob Nodes
- node-01                                                   - node-01
- node-02                                                   - node-02
```