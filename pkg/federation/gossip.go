package federation

import (
	"collective/pkg/protocol"
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GossipService implements epidemic-style gossip protocol for peer discovery and state sharing
type GossipService struct {
	mu sync.RWMutex

	localDomain  string
	localAddress *FederatedAddress
	peers        map[string]*PeerState     // domain -> peer state
	dataStores   map[string]*DataStoreInfo // path -> datastore info

	// Gossip configuration
	gossipPeriod time.Duration
	fanout       int // Number of peers to gossip to

	// Lamport clock for versioning
	version       uint64
	versionVector map[string]uint64 // domain -> version

	// Callbacks
	onPeerJoin        func(peer *PeerState)
	onPeerLeave       func(domain string)
	onDataStoreUpdate func(ds *DataStoreInfo)

	// Network
	connections map[string]*grpc.ClientConn
	grpcServer  *grpc.Server
	trustStore  *TrustStore // For secure connections
	certPath    string      // Client certificate path
	keyPath     string      // Client key path

	stopCh chan struct{}
}

// PeerState represents the state of a federation peer
type PeerState struct {
	Address    *FederatedAddress
	Endpoints  []*PeerEndpoint
	LastSeen   time.Time
	Version    uint64 // Lamport clock
	DataStores []string
	Status     PeerStatus
}

// PeerEndpoint represents network endpoints for a peer
type PeerEndpoint struct {
	Domain     string
	DirectIPs  []string
	VPNIPs     []string
	LANIPs     []string
	LastSeen   time.Time
	Latency    time.Duration
	Preference int
}

// PeerStatus represents the health status of a peer
type PeerStatus int

const (
	PeerUnknown PeerStatus = iota
	PeerAlive
	PeerSuspected
	PeerDead
)

// NewGossipService creates a new gossip service
func NewGossipService(localDomain string, trustStore *TrustStore, certPath, keyPath string) (*GossipService, error) {
	addr, err := ParseAddress(fmt.Sprintf("coord@%s", localDomain))
	if err != nil {
		return nil, fmt.Errorf("invalid local domain: %w", err)
	}

	return &GossipService{
		localDomain:   localDomain,
		localAddress:  addr,
		peers:         make(map[string]*PeerState),
		dataStores:    make(map[string]*DataStoreInfo),
		gossipPeriod:  30 * time.Second,
		fanout:        3,
		version:       0,
		versionVector: make(map[string]uint64),
		connections:   make(map[string]*grpc.ClientConn),
		trustStore:    trustStore,
		certPath:      certPath,
		keyPath:       keyPath,
		stopCh:        make(chan struct{}),
	}, nil
}

// Start begins the gossip protocol
func (g *GossipService) Start() error {
	// Start periodic gossip
	go g.gossipLoop()

	// Start anti-entropy process
	go g.antiEntropyLoop()

	// Start failure detector
	go g.failureDetectorLoop()

	return nil
}

// Stop halts the gossip protocol
func (g *GossipService) Stop() {
	close(g.stopCh)

	// Close all connections
	g.mu.Lock()
	for _, conn := range g.connections {
		conn.Close()
	}
	g.mu.Unlock()
}

// AddPeer adds a new peer to gossip with
func (g *GossipService) AddPeer(address *FederatedAddress, endpoints []*PeerEndpoint) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	peer := &PeerState{
		Address:    address,
		Endpoints:  endpoints,
		LastSeen:   time.Now(),
		Version:    0,
		Status:     PeerAlive,
		DataStores: []string{},
	}

	g.peers[address.Domain] = peer
	g.incrementVersion()

	// Trigger callback
	if g.onPeerJoin != nil {
		go g.onPeerJoin(peer)
	}

	return nil
}

// RemovePeer removes a peer from gossip
func (g *GossipService) RemovePeer(domain string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.peers, domain)
	g.incrementVersion()

	// Close connection if exists
	if conn, exists := g.connections[domain]; exists {
		conn.Close()
		delete(g.connections, domain)
	}

	// Trigger callback
	if g.onPeerLeave != nil {
		go g.onPeerLeave(domain)
	}
}

// GetHealthyPeers returns all healthy peers
func (g *GossipService) GetHealthyPeers() []*PeerState {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var healthy []*PeerState
	for _, peer := range g.peers {
		if peer.Status == PeerAlive {
			healthy = append(healthy, peer)
		}
	}
	return healthy
}

// gossipLoop performs periodic gossip rounds
func (g *GossipService) gossipLoop() {
	ticker := time.NewTicker(g.gossipPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.performGossipRound()
		case <-g.stopCh:
			return
		}
	}
}

// performGossipRound executes one round of gossip
func (g *GossipService) performGossipRound() {
	g.mu.RLock()

	// Select random peers for gossip (epidemic style)
	peers := g.selectGossipPeers()

	// Prepare gossip message
	msg := g.prepareGossipMessage()

	g.mu.RUnlock()

	// Send gossip to selected peers
	for _, peer := range peers {
		go g.gossipToPeer(peer, msg)
	}
}

// selectGossipPeers randomly selects peers for gossip
func (g *GossipService) selectGossipPeers() []*PeerState {
	var alivePeers []*PeerState
	for _, peer := range g.peers {
		if peer.Status == PeerAlive {
			alivePeers = append(alivePeers, peer)
		}
	}

	// Shuffle and select up to fanout peers
	rand.Shuffle(len(alivePeers), func(i, j int) {
		alivePeers[i], alivePeers[j] = alivePeers[j], alivePeers[i]
	})

	count := g.fanout
	if count > len(alivePeers) {
		count = len(alivePeers)
	}

	return alivePeers[:count]
}

// prepareGossipMessage creates a gossip message with current state
func (g *GossipService) prepareGossipMessage() *protocol.GossipMessage {
	// Create heartbeat payload with version vector
	heartbeat := &protocol.HeartbeatPayload{
		KnownPeers:   make([]string, 0),
		PeerVersions: make(map[string]uint64),
	}

	for domain, peer := range g.peers {
		heartbeat.KnownPeers = append(heartbeat.KnownPeers, domain)
		heartbeat.PeerVersions[domain] = peer.Version
	}

	return &protocol.GossipMessage{
		Type:          protocol.GossipType_GOSSIP_HEARTBEAT,
		SourceAddress: g.localAddress.String(),
		Version:       g.version,
		Timestamp:     timestamppb.Now(),
		Payload: &protocol.GossipMessage_Heartbeat{
			Heartbeat: heartbeat,
		},
	}
}

// gossipToPeer sends gossip to a specific peer
func (g *GossipService) gossipToPeer(peer *PeerState, msg *protocol.GossipMessage) {
	conn, err := g.getConnection(peer)
	if err != nil {
		// Mark peer as suspected if can't connect
		g.updatePeerStatus(peer.Address.Domain, PeerSuspected)
		return
	}

	client := protocol.NewGossipServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := &protocol.GossipRequest{
		SenderDomain:  g.localDomain,
		Messages:      []*protocol.GossipMessage{msg},
		VersionVector: g.versionVector,
	}

	resp, err := client.GossipExchange(ctx, req)
	if err != nil {
		g.updatePeerStatus(peer.Address.Domain, PeerSuspected)
		return
	}

	// Process response
	g.processGossipResponse(resp)

	// Mark peer as alive
	g.updatePeerStatus(peer.Address.Domain, PeerAlive)
}

// processGossipResponse handles incoming gossip messages
func (g *GossipService) processGossipResponse(resp *protocol.GossipResponse) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, msg := range resp.Messages {
		g.processGossipMessage(msg)
	}

	// Update version vector for anti-entropy
	for domain, version := range resp.VersionVector {
		if version > g.versionVector[domain] {
			g.versionVector[domain] = version
		}
	}

	// Check if sync needed
	if resp.NeedSync {
		go g.performAntiEntropy(resp.ResponderDomain)
	}
}

// processGossipMessage handles a single gossip message
func (g *GossipService) processGossipMessage(msg *protocol.GossipMessage) {
	// Update Lamport clock
	if msg.Version > g.version {
		g.version = msg.Version
	}
	g.version++

	switch msg.Type {
	case protocol.GossipType_GOSSIP_JOIN:
		g.handleJoinMessage(msg)
	case protocol.GossipType_GOSSIP_LEAVE:
		g.handleLeaveMessage(msg)
	case protocol.GossipType_GOSSIP_UPDATE:
		g.handleUpdateMessage(msg)
	case protocol.GossipType_GOSSIP_HEARTBEAT:
		g.handleHeartbeatMessage(msg)
	case protocol.GossipType_GOSSIP_SYNC:
		g.handleSyncMessage(msg)
	}
}

// handleJoinMessage processes a join message
func (g *GossipService) handleJoinMessage(msg *protocol.GossipMessage) {
	join := msg.GetJoin()
	if join == nil {
		return
	}

	// Parse federated address
	addr, err := ParseAddress(msg.SourceAddress)
	if err != nil {
		return
	}

	// Convert protobuf endpoints to our format
	endpoints := make([]*PeerEndpoint, len(join.Endpoints))
	for i, ep := range join.Endpoints {
		endpoints[i] = &PeerEndpoint{
			Domain:     ep.Domain,
			DirectIPs:  ep.DirectIps,
			VPNIPs:     ep.VpnIps,
			LANIPs:     ep.LanIps,
			Latency:    time.Duration(ep.LatencyMs) * time.Millisecond,
			Preference: int(ep.Preference),
			LastSeen:   time.Now(),
		}
	}

	// Add or update peer
	peer := &PeerState{
		Address:   addr,
		Endpoints: endpoints,
		LastSeen:  msg.Timestamp.AsTime(),
		Version:   msg.Version,
		Status:    PeerAlive,
	}

	g.peers[addr.Domain] = peer

	// Trigger callback
	if g.onPeerJoin != nil {
		go g.onPeerJoin(peer)
	}
}

// handleHeartbeatMessage processes heartbeat messages
func (g *GossipService) handleHeartbeatMessage(msg *protocol.GossipMessage) {
	heartbeat := msg.GetHeartbeat()
	if heartbeat == nil {
		return
	}

	// Parse source address
	addr, err := ParseAddress(msg.SourceAddress)
	if err != nil {
		return
	}

	// Update peer's last seen time
	if peer, exists := g.peers[addr.Domain]; exists {
		peer.LastSeen = time.Now()
		peer.Version = msg.Version
		peer.Status = PeerAlive
	}

	// Check for unknown peers in heartbeat
	for _, peerDomain := range heartbeat.KnownPeers {
		if _, exists := g.peers[peerDomain]; !exists && peerDomain != g.localDomain {
			// We don't know about this peer, might need sync
			g.versionVector[peerDomain] = 0
		}
	}
}

// antiEntropyLoop performs periodic anti-entropy synchronization
func (g *GossipService) antiEntropyLoop() {
	ticker := time.NewTicker(g.gossipPeriod * 3) // Less frequent than gossip
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.performAntiEntropyRound()
		case <-g.stopCh:
			return
		}
	}
}

// performAntiEntropyRound checks for inconsistencies and syncs
func (g *GossipService) performAntiEntropyRound() {
	g.mu.RLock()
	peers := g.selectRandomPeer()
	g.mu.RUnlock()

	if peers != nil {
		g.performAntiEntropy(peers.Address.Domain)
	}
}

// performAntiEntropy syncs with a specific peer
func (g *GossipService) performAntiEntropy(peerDomain string) {
	g.mu.RLock()
	peer, exists := g.peers[peerDomain]
	if !exists {
		g.mu.RUnlock()
		return
	}
	g.mu.RUnlock()

	conn, err := g.getConnection(peer)
	if err != nil {
		return
	}

	client := protocol.NewGossipServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Send sync request
	syncMsg := &protocol.GossipMessage{
		Type:          protocol.GossipType_GOSSIP_SYNC,
		SourceAddress: g.localAddress.String(),
		Version:       g.version,
		Timestamp:     timestamppb.Now(),
		Payload: &protocol.GossipMessage_Sync{
			Sync: &protocol.SyncPayload{
				HaveVersions: g.versionVector,
				NeedPeers:    []string{}, // Will be filled based on missing peers
			},
		},
	}

	req := &protocol.GossipRequest{
		SenderDomain:  g.localDomain,
		Messages:      []*protocol.GossipMessage{syncMsg},
		VersionVector: g.versionVector,
	}

	resp, err := client.GossipExchange(ctx, req)
	if err != nil {
		return
	}

	g.processGossipResponse(resp)
}

// failureDetectorLoop monitors peer health
func (g *GossipService) failureDetectorLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	suspectTimeout := g.gossipPeriod * 3
	deadTimeout := g.gossipPeriod * 6

	for {
		select {
		case <-ticker.C:
			g.detectFailures(suspectTimeout, deadTimeout)
		case <-g.stopCh:
			return
		}
	}
}

// detectFailures marks peers as suspected or dead based on last seen time
func (g *GossipService) detectFailures(suspectTimeout, deadTimeout time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	for domain, peer := range g.peers {
		elapsed := now.Sub(peer.LastSeen)

		// Check dead timeout first, then suspect
		if elapsed > deadTimeout {
			if peer.Status != PeerDead {
				peer.Status = PeerDead
				if g.onPeerLeave != nil {
					go g.onPeerLeave(domain)
				}
			}
		} else if elapsed > suspectTimeout {
			if peer.Status == PeerAlive {
				peer.Status = PeerSuspected
			}
		}
	}
}

// Helper methods

func (g *GossipService) incrementVersion() {
	g.version++
	g.versionVector[g.localDomain] = g.version
}

func (g *GossipService) updatePeerStatus(domain string, status PeerStatus) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if peer, exists := g.peers[domain]; exists {
		peer.Status = status
		if status == PeerAlive {
			peer.LastSeen = time.Now()
		}
	}
}

func (g *GossipService) selectRandomPeer() *PeerState {
	var alivePeers []*PeerState
	for _, peer := range g.peers {
		if peer.Status == PeerAlive {
			alivePeers = append(alivePeers, peer)
		}
	}

	if len(alivePeers) == 0 {
		return nil
	}

	return alivePeers[rand.Intn(len(alivePeers))]
}

func (g *GossipService) getConnection(peer *PeerState) (*grpc.ClientConn, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if we have an existing connection
	if conn, exists := g.connections[peer.Address.Domain]; exists {
		return conn, nil
	}

	// Try to establish connection using available endpoints
	for _, endpoint := range peer.Endpoints {
		// Try LAN first, then direct, then VPN
		addresses := append(endpoint.LANIPs, endpoint.DirectIPs...)
		addresses = append(addresses, endpoint.VPNIPs...)

		for _, addr := range addresses {
			var conn *grpc.ClientConn
			var err error

			// Use secure connection if trust store is available
			if g.trustStore != nil && g.certPath != "" && g.keyPath != "" {
				tlsConfig, tlsErr := g.trustStore.GetClientTLSConfig(addr, g.certPath, g.keyPath)
				if tlsErr == nil {
					conn, err = grpc.Dial(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
				} else {
					// Fall back to insecure if TLS setup fails
					conn, err = grpc.Dial(addr, grpc.WithInsecure())
				}
			} else {
				// No trust store or certificates, use insecure connection
				conn, err = grpc.Dial(addr, grpc.WithInsecure())
			}

			if err == nil {
				g.connections[peer.Address.Domain] = conn
				return conn, nil
			}
		}
	}

	return nil, fmt.Errorf("failed to connect to peer %s", peer.Address.Domain)
}

// Stub implementations for missing message handlers
func (g *GossipService) handleLeaveMessage(msg *protocol.GossipMessage) {
	leave := msg.GetLeave()
	if leave == nil {
		return
	}

	g.RemovePeer(leave.MemberDomain)
}

func (g *GossipService) handleUpdateMessage(msg *protocol.GossipMessage) {
	// TODO: Implement DataStore updates
}

func (g *GossipService) handleSyncMessage(msg *protocol.GossipMessage) {
	// TODO: Implement full state sync
}

// SetCallbacks sets event callbacks
func (g *GossipService) SetCallbacks(
	onJoin func(*PeerState),
	onLeave func(string),
	onUpdate func(*DataStoreInfo),
) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.onPeerJoin = onJoin
	g.onPeerLeave = onLeave
	g.onDataStoreUpdate = onUpdate
}

// Broadcast sends a message to all known peers
func (g *GossipService) Broadcast(msg interface{}) {
	// TODO: Implement broadcast for important updates
}
