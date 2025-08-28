package coordinator

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"collective/pkg/auth"
	"collective/pkg/config"
	"collective/pkg/federation"
	"collective/pkg/protocol"
	"collective/pkg/storage"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type Coordinator struct {
	protocol.UnimplementedCoordinatorServer

	memberID   types.MemberID
	address    string
	logger     *zap.Logger
	config     *config.CoordinatorConfig
	authConfig *auth.AuthConfig

	// Peer management
	peers     map[types.MemberID]*Peer
	peerMutex sync.RWMutex

	// Node management
	nodes     map[types.NodeID]*types.StorageNode
	nodeMutex sync.RWMutex

	// File metadata
	files     map[types.FileID]*types.File
	fileMutex sync.RWMutex

	// Directory tree metadata
	directories    map[string]*types.Directory
	fileEntries    map[string]*types.FileEntry
	directoryMutex sync.RWMutex

	// Per-file locks for concurrent write protection
	fileLocks     map[string]*sync.RWMutex
	fileLockMutex sync.Mutex

	// Chunk management
	chunkManager     *storage.ChunkManager
	chunkAllocations map[types.ChunkID][]types.NodeID // Which nodes have which chunks
	fileChunks       map[string][]types.ChunkID       // File path to chunk IDs
	chunkMutex       sync.RWMutex

	// Write coalescing for small writes
	writeBuffers     map[string]*WriteBuffer
	writeBufferMutex sync.RWMutex

	server   *grpc.Server
	listener net.Listener

	// Bootstrap server for invite redemption (runs on insecure port)
	bootstrapServer   *grpc.Server
	bootstrapListener net.Listener

	// Invite management
	inviteManager *federation.InviteManager

	ctx    context.Context
	cancel context.CancelFunc
}

type WriteBuffer struct {
	data         []byte
	lastWrite    time.Time
	flushTimer   *time.Timer
	flushSize    int64
	flushTimeout time.Duration
}

const (
	WriteBufferFlushSize    = 256 * 1024             // Flush after 256KB
	WriteBufferFlushTimeout = 100 * time.Millisecond // Flush after 100ms of inactivity
)

type Peer struct {
	MemberID   types.MemberID
	Address    string
	Client     protocol.CoordinatorClient
	Connection *grpc.ClientConn
	LastSeen   time.Time
	IsHealthy  bool
}

func New(cfg *config.CoordinatorConfig, memberID string, logger *zap.Logger) *Coordinator {
	return NewWithAuth(cfg, memberID, logger, nil)
}

func NewWithAuth(cfg *config.CoordinatorConfig, memberID string, logger *zap.Logger, authConfig *auth.AuthConfig) *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())

	return &Coordinator{
		memberID:         types.MemberID(memberID),
		address:          cfg.Address,
		logger:           logger,
		config:           cfg,
		authConfig:       authConfig,
		peers:            make(map[types.MemberID]*Peer),
		nodes:            make(map[types.NodeID]*types.StorageNode),
		files:            make(map[types.FileID]*types.File),
		directories:      make(map[string]*types.Directory),
		fileEntries:      make(map[string]*types.FileEntry),
		fileLocks:        make(map[string]*sync.RWMutex),
		chunkManager:     storage.NewChunkManager(),
		chunkAllocations: make(map[types.ChunkID][]types.NodeID),
		fileChunks:       make(map[string][]types.ChunkID),
		writeBuffers:     make(map[string]*WriteBuffer),
		inviteManager:    federation.NewInviteManager(),
		ctx:              ctx,
		cancel:           cancel,
	}
}

func (c *Coordinator) Start() error {
	listener, err := net.Listen("tcp", c.address)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", c.address, err)
	}

	c.listener = listener

	// Create gRPC server with authentication if configured
	var serverOpts []grpc.ServerOption

	if c.authConfig != nil && c.authConfig.Enabled {
		// Create TLS configuration
		tlsBuilder, err := auth.NewTLSConfigBuilder(c.authConfig)
		if err != nil {
			return fmt.Errorf("failed to create TLS config: %w", err)
		}

		tlsConfig, err := tlsBuilder.BuildServerConfig()
		if err != nil {
			return fmt.Errorf("failed to build server TLS config: %w", err)
		}

		if tlsConfig != nil {
			creds := credentials.NewTLS(tlsConfig)
			serverOpts = append(serverOpts, grpc.Creds(creds))
			c.logger.Info("TLS enabled for coordinator", zap.String("member_id", string(c.memberID)))
		}

		// Add authentication interceptors if required
		if c.authConfig.RequireClientAuth {
			// Create authenticator and interceptor
			authInterceptor := auth.NewAuthInterceptor(nil, nil, true)
			serverOpts = append(serverOpts,
				grpc.UnaryInterceptor(authInterceptor.UnaryServerInterceptor()),
				grpc.StreamInterceptor(authInterceptor.StreamServerInterceptor()),
			)
			c.logger.Info("Client authentication required", zap.String("member_id", string(c.memberID)))
		}
	}

	c.server = grpc.NewServer(serverOpts...)
	protocol.RegisterCoordinatorServer(c.server, c)

	c.logger.Info("Coordinator starting", zap.String("address", c.address), zap.String("member_id", string(c.memberID)))

	// Initialize root directory
	c.initializeRootDirectory()

	// Start bootstrap server for invite redemption (insecure)
	if c.authConfig != nil && c.authConfig.Enabled {
		// Extract port from address
		host, port, _ := net.SplitHostPort(c.address)
		portNum := 8080 // Default bootstrap port
		if p, err := strconv.Atoi(port); err == nil {
			portNum = p + 1000 // Bootstrap port is main port + 1000
		}
		bootstrapAddr := net.JoinHostPort(host, strconv.Itoa(portNum))

		bootstrapListener, err := net.Listen("tcp", bootstrapAddr)
		if err != nil {
			c.logger.Warn("Failed to start bootstrap server",
				zap.String("address", bootstrapAddr),
				zap.Error(err))
		} else {
			c.bootstrapListener = bootstrapListener
			c.bootstrapServer = grpc.NewServer(grpc.MaxRecvMsgSize(10 * 1024 * 1024)) // 10MB max for certificates

			// Register a limited coordinator that only handles invite operations
			protocol.RegisterCoordinatorServer(c.bootstrapServer, &BootstrapCoordinator{
				coordinator: c,
				logger:      c.logger.With(zap.String("server", "bootstrap")),
			})

			c.logger.Info("Bootstrap server started for invite redemption",
				zap.String("address", bootstrapAddr),
				zap.String("member_id", string(c.memberID)))

			go func() {
				if err := c.bootstrapServer.Serve(bootstrapListener); err != nil {
					c.logger.Error("Bootstrap server failed", zap.Error(err))
				}
			}()
		}
	}

	// Start background tasks
	go c.heartbeatLoop()
	go c.syncLoop()
	go c.nodeHealthLoop()

	return c.server.Serve(listener)
}

// GenerateInvite creates a new invitation code
func (c *Coordinator) GenerateInvite(ctx context.Context, req *protocol.GenerateInviteRequest) (*protocol.GenerateInviteResponse, error) {
	c.logger.Info("GenerateInvite request received")

	if c.inviteManager == nil {
		return &protocol.GenerateInviteResponse{
			Success: false,
			Message: "Invite system not available",
		}, nil
	}

	// Get the inviter from context (in production, extract from client cert)
	// For now, use the coordinator's member ID
	inviter := &federation.FederatedAddress{
		LocalPart: "coordinator",
		Domain:    string(c.memberID) + ".collective.local",
	}

	// Convert proto grants to federation grants
	grants := make([]federation.DataStoreGrant, 0, len(req.Grants))
	for _, g := range req.Grants {
		rights := make([]federation.Right, 0, len(g.Rights))
		for _, r := range g.Rights {
			rights = append(rights, federation.Right(r))
		}
		grants = append(grants, federation.DataStoreGrant{
			Path:   g.Path,
			Rights: rights,
		})
	}

	// Generate the invite
	validity := time.Duration(req.ValiditySeconds) * time.Second
	invite, err := c.inviteManager.GenerateInvite(inviter, grants, validity, int(req.MaxUses))
	if err != nil {
		c.logger.Error("Failed to generate invite", zap.Error(err))
		return &protocol.GenerateInviteResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to generate invite: %v", err),
		}, nil
	}

	// Create share URL
	shareURL := fmt.Sprintf("collective://join/%s:8001/%s", c.memberID, invite.Code)

	c.logger.Info("Successfully generated invite",
		zap.String("code", invite.Code),
		zap.String("inviter", inviter.String()),
		zap.Int("max_uses", int(req.MaxUses)))

	return &protocol.GenerateInviteResponse{
		Success:   true,
		Code:      invite.Code,
		Message:   "Invite generated successfully",
		ExpiresAt: invite.ExpiresAt.Unix(),
		ShareUrl:  shareURL,
	}, nil
}

func (c *Coordinator) Stop() {
	c.cancel()

	// Disconnect from all peers
	c.peerMutex.Lock()
	for _, peer := range c.peers {
		if peer.Connection != nil {
			peer.Connection.Close()
		}
	}
	c.peerMutex.Unlock()

	if c.bootstrapServer != nil {
		c.bootstrapServer.GracefulStop()
	}

	if c.server != nil {
		c.server.GracefulStop()
	}
}

func (c *Coordinator) ConnectToPeer(memberID, address string) error {
	// Check if already connected
	c.peerMutex.RLock()
	if _, exists := c.peers[types.MemberID(memberID)]; exists {
		c.peerMutex.RUnlock()
		c.logger.Debug("Already connected to peer", zap.String("member_id", memberID))
		return nil
	}
	c.peerMutex.RUnlock()

	// Use secure connection if auth is configured
	var dialOpts []grpc.DialOption
	if c.authConfig != nil && c.authConfig.Enabled {
		tlsBuilder, err := auth.NewTLSConfigBuilder(c.authConfig)
		if err == nil {
			// For peer connections, use BuildPeerConfig which handles mutual authentication
			tlsConfig, err := tlsBuilder.BuildPeerConfig()
			if err == nil && tlsConfig != nil {
				dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
			}
		}
	}

	// Fallback to insecure if TLS not configured
	if len(dialOpts) == 0 {
		dialOpts = append(dialOpts, grpc.WithInsecure())
	}

	conn, err := grpc.Dial(address, dialOpts...)
	if err != nil {
		return fmt.Errorf("failed to connect to peer %s at %s: %w", memberID, address, err)
	}

	client := protocol.NewCoordinatorClient(conn)

	// Get our node list to share
	c.nodeMutex.RLock()
	nodeInfos := make([]*protocol.NodeInfo, 0, len(c.nodes))
	for _, node := range c.nodes {
		nodeInfos = append(nodeInfos, &protocol.NodeInfo{
			NodeId:        string(node.ID),
			MemberId:      string(node.MemberID),
			Address:       node.Address,
			TotalCapacity: node.TotalCapacity,
			UsedCapacity:  node.UsedCapacity,
			IsHealthy:     node.IsHealthy,
		})
	}
	c.nodeMutex.RUnlock()

	// Attempt to connect
	resp, err := client.PeerConnect(c.ctx, &protocol.PeerConnectRequest{
		MemberId: string(c.memberID),
		Address:  c.address,
		Nodes:    nodeInfos,
	})

	if err != nil {
		conn.Close()
		return fmt.Errorf("peer connect request failed: %w", err)
	}

	if !resp.Accepted {
		conn.Close()
		return fmt.Errorf("peer %s rejected connection", memberID)
	}

	// Store peer connection
	peer := &Peer{
		MemberID:   types.MemberID(memberID),
		Address:    address,
		Client:     client,
		Connection: conn,
		LastSeen:   time.Now(),
		IsHealthy:  true,
	}

	c.peerMutex.Lock()
	c.peers[peer.MemberID] = peer
	c.peerMutex.Unlock()

	// Store their nodes
	for _, nodeInfo := range resp.Nodes {
		c.addRemoteNode(nodeInfo)
	}

	c.logger.Info("Connected to peer", zap.String("member_id", memberID), zap.String("address", address))
	return nil
}

func (c *Coordinator) PeerConnect(ctx context.Context, req *protocol.PeerConnectRequest) (*protocol.PeerConnectResponse, error) {
	c.logger.Info("Peer connection request", zap.String("from", req.MemberId))

	// Store peer info
	memberID := types.MemberID(req.MemberId)

	// Check if we already have this peer
	c.peerMutex.RLock()
	existingPeer, alreadyConnected := c.peers[memberID]
	c.peerMutex.RUnlock()

	if alreadyConnected {
		c.logger.Debug("Already connected to peer", zap.String("peer", req.MemberId))
		// Update the address if it changed
		if existingPeer.Address != req.Address {
			existingPeer.Address = req.Address
		}
	} else {
		// Use secure connection if auth is configured
		var dialOpts []grpc.DialOption
		if c.authConfig != nil && c.authConfig.Enabled {
			tlsBuilder, err := auth.NewTLSConfigBuilder(c.authConfig)
			if err == nil {
				tlsConfig, err := tlsBuilder.BuildClientConfig()
				if err == nil && tlsConfig != nil {
					// For peer connections, load all member CAs for trust
					certDir := filepath.Dir(c.authConfig.CAPath)
					if multiCAPool, err := tlsBuilder.LoadMultiCAPool(certDir); err == nil {
						tlsConfig.RootCAs = multiCAPool
					}
					dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
				}
			}
		}

		// Fallback to insecure if TLS not configured
		if len(dialOpts) == 0 {
			dialOpts = append(dialOpts, grpc.WithInsecure())
		}

		conn, err := grpc.Dial(req.Address, dialOpts...)
		if err != nil {
			return &protocol.PeerConnectResponse{Accepted: false}, nil
		}

		peer := &Peer{
			MemberID:   memberID,
			Address:    req.Address,
			Client:     protocol.NewCoordinatorClient(conn),
			Connection: conn,
			LastSeen:   time.Now(),
			IsHealthy:  true,
		}

		c.peerMutex.Lock()
		c.peers[memberID] = peer
		c.peerMutex.Unlock()
	}

	// Store their nodes
	for _, nodeInfo := range req.Nodes {
		c.addRemoteNode(nodeInfo)
	}

	// If bidirectional peering is enabled and we don't already have a reverse connection,
	// establish one back to the requesting peer
	if c.config.BidirectionalPeering && !alreadyConnected {
		go func() {
			time.Sleep(1 * time.Second) // Small delay to let the other side complete
			c.logger.Info("Establishing bidirectional connection back to peer",
				zap.String("peer", req.MemberId),
				zap.String("address", req.Address))

			if err := c.ConnectToPeer(string(memberID), req.Address); err != nil {
				c.logger.Warn("Failed to establish bidirectional connection",
					zap.String("peer", req.MemberId),
					zap.Error(err))
			}
		}()
	}

	// Prepare our node list
	c.nodeMutex.RLock()
	nodeInfos := make([]*protocol.NodeInfo, 0, len(c.nodes))
	for _, node := range c.nodes {
		if node.MemberID == c.memberID { // Only share our own nodes
			nodeInfos = append(nodeInfos, &protocol.NodeInfo{
				NodeId:        string(node.ID),
				MemberId:      string(node.MemberID),
				Address:       node.Address,
				TotalCapacity: node.TotalCapacity,
				UsedCapacity:  node.UsedCapacity,
				IsHealthy:     node.IsHealthy,
			})
		}
	}
	c.nodeMutex.RUnlock()

	return &protocol.PeerConnectResponse{
		Accepted: true,
		MemberId: string(c.memberID),
		Nodes:    nodeInfos,
	}, nil
}

func (c *Coordinator) PeerDisconnect(ctx context.Context, req *protocol.PeerDisconnectRequest) (*protocol.PeerDisconnectResponse, error) {
	memberID := types.MemberID(req.MemberId)

	c.peerMutex.Lock()
	if peer, exists := c.peers[memberID]; exists {
		if peer.Connection != nil {
			peer.Connection.Close()
		}
		delete(c.peers, memberID)
	}
	c.peerMutex.Unlock()

	c.logger.Info("Peer disconnected", zap.String("member_id", req.MemberId))
	return &protocol.PeerDisconnectResponse{Success: true}, nil
}

func (c *Coordinator) Heartbeat(ctx context.Context, req *protocol.HeartbeatRequest) (*protocol.HeartbeatResponse, error) {
	memberID := types.MemberID(req.MemberId)

	// Handle peer heartbeat
	c.peerMutex.Lock()
	if peer, exists := c.peers[memberID]; exists {
		peer.LastSeen = time.Now()
		peer.IsHealthy = true
	}
	c.peerMutex.Unlock()

	// Handle node heartbeat if NodeInfo is provided
	if req.NodeInfo != nil {
		nodeID := types.NodeID(req.NodeInfo.NodeId)

		c.nodeMutex.Lock()

		if node, exists := c.nodes[nodeID]; exists {
			// Update existing node's health status
			node.UsedCapacity = req.NodeInfo.UsedCapacity
			node.IsHealthy = req.NodeInfo.IsHealthy
			node.LastHealthCheck = time.Now()
			c.nodeMutex.Unlock()

			c.logger.Debug("Node heartbeat received",
				zap.String("node_id", string(nodeID)),
				zap.Int64("used_capacity", req.NodeInfo.UsedCapacity))

			return &protocol.HeartbeatResponse{
				MemberId:  string(c.memberID),
				Timestamp: time.Now().Unix(),
				Success:   true,
			}, nil
		} else {
			c.nodeMutex.Unlock()

			// Node not found - it needs to re-register
			c.logger.Warn("Heartbeat from unregistered node",
				zap.String("node_id", string(nodeID)))

			// Auto-register the node if it's from our member
			if req.NodeInfo.MemberId == string(c.memberID) {
				c.registerNodeInternal(req.NodeInfo.NodeId, req.NodeInfo.Address, req.NodeInfo.TotalCapacity)
				c.logger.Info("Auto-registered node from heartbeat",
					zap.String("node_id", string(nodeID)))

				return &protocol.HeartbeatResponse{
					MemberId:  string(c.memberID),
					Timestamp: time.Now().Unix(),
					Success:   true,
				}, nil
			}

			return &protocol.HeartbeatResponse{
				MemberId:  string(c.memberID),
				Timestamp: time.Now().Unix(),
				Success:   false, // Node needs to register
			}, nil
		}
	}

	return &protocol.HeartbeatResponse{
		MemberId:  string(c.memberID),
		Timestamp: time.Now().Unix(),
		Success:   true,
	}, nil
}

func (c *Coordinator) addRemoteNode(nodeInfo *protocol.NodeInfo) {
	node := &types.StorageNode{
		ID:              types.NodeID(nodeInfo.NodeId),
		MemberID:        types.MemberID(nodeInfo.MemberId),
		Address:         nodeInfo.Address,
		TotalCapacity:   nodeInfo.TotalCapacity,
		UsedCapacity:    nodeInfo.UsedCapacity,
		IsHealthy:       nodeInfo.IsHealthy,
		LastHealthCheck: time.Now(),
	}

	c.nodeMutex.Lock()
	c.nodes[node.ID] = node
	c.nodeMutex.Unlock()
}

func (c *Coordinator) heartbeatLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.sendHeartbeats()
		}
	}
}

func (c *Coordinator) sendHeartbeats() {
	c.peerMutex.RLock()
	peers := make([]*Peer, 0, len(c.peers))
	for _, peer := range c.peers {
		peers = append(peers, peer)
	}
	c.peerMutex.RUnlock()

	for _, peer := range peers {
		go func(p *Peer) {
			ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
			defer cancel()

			_, err := p.Client.Heartbeat(ctx, &protocol.HeartbeatRequest{
				MemberId:  string(c.memberID),
				Timestamp: time.Now().Unix(),
			})

			c.peerMutex.Lock()
			if err != nil {
				p.IsHealthy = false
				c.logger.Warn("Heartbeat failed", zap.String("peer", string(p.MemberID)), zap.Error(err))
			} else {
				p.IsHealthy = true
				p.LastSeen = time.Now()
			}
			c.peerMutex.Unlock()
		}(peer)
	}
}

// StoreFile handles file storage across the collective
func (c *Coordinator) StoreFile(ctx context.Context, req *protocol.StoreFileRequest) (*protocol.StoreFileResponse, error) {
	c.logger.Info("Storing file",
		zap.String("file_id", req.FileId),
		zap.Int("size", len(req.Data)))

	// Create chunk manager
	cm := storage.NewChunkManager()

	// Split file into chunks
	fileID := types.FileID(req.FileId)
	chunks, err := cm.SplitIntoChunks(req.Data, fileID)
	if err != nil {
		return &protocol.StoreFileResponse{Success: false}, err
	}

	// Get available nodes
	c.nodeMutex.RLock()
	availableNodes := make([]*types.StorageNode, 0, len(c.nodes))
	for _, node := range c.nodes {
		if node.IsHealthy {
			availableNodes = append(availableNodes, node)
		}
	}
	c.nodeMutex.RUnlock()

	if len(availableNodes) == 0 {
		return &protocol.StoreFileResponse{Success: false}, fmt.Errorf("no healthy nodes available")
	}

	// Create distribution strategy with 2x replication
	ds := storage.NewDistributionStrategy(2)
	allocations, err := ds.AllocateChunks(chunks, availableNodes)
	if err != nil {
		return &protocol.StoreFileResponse{Success: false}, err
	}

	// Ensure member diversity
	allocations = ds.EnsureMemberDiversity(allocations, availableNodes)

	// Store chunks on allocated nodes
	var locations []*protocol.ChunkLocation
	successCount := 0

	for _, chunk := range chunks {
		nodeIDs := allocations[chunk.ID]
		for _, nodeID := range nodeIDs {
			// Find node
			var targetNode *types.StorageNode
			c.nodeMutex.RLock()
			targetNode = c.nodes[nodeID]
			c.nodeMutex.RUnlock()

			if targetNode == nil {
				continue
			}

			// Connect to node and store chunk
			if err := c.storeChunkOnNode(ctx, targetNode, chunk); err != nil {
				c.logger.Warn("Failed to store chunk on node",
					zap.String("chunk_id", string(chunk.ID)),
					zap.String("node_id", string(nodeID)),
					zap.Error(err))
				continue
			}

			locations = append(locations, &protocol.ChunkLocation{
				ChunkId: string(chunk.ID),
				NodeId:  string(nodeID),
				Index:   int32(chunk.Index),
				Size:    chunk.Size,
			})
			successCount++

			c.logger.Debug("Chunk stored",
				zap.String("chunk_id", string(chunk.ID)),
				zap.String("node_id", string(nodeID)))
		}
	}

	if successCount == 0 {
		return &protocol.StoreFileResponse{Success: false}, fmt.Errorf("failed to store any chunks")
	}

	// Store file metadata
	file := &types.File{
		ID:        fileID,
		Name:      req.Filename,
		Size:      int64(len(req.Data)),
		CreatedAt: time.Now(),
		OwnerID:   c.memberID,
	}

	for _, loc := range locations {
		file.Chunks = append(file.Chunks, types.ChunkLocation{
			ChunkID: types.ChunkID(loc.ChunkId),
			NodeID:  types.NodeID(loc.NodeId),
			Index:   int(loc.Index),
			Size:    loc.Size,
		})
	}

	c.fileMutex.Lock()
	c.files[fileID] = file
	c.fileMutex.Unlock()

	c.logger.Info("File stored successfully",
		zap.String("file_id", req.FileId),
		zap.Int("chunks", len(chunks)),
		zap.Int("replicas", successCount))

	return &protocol.StoreFileResponse{
		Success:   true,
		FileId:    req.FileId,
		Locations: locations,
	}, nil
}

func (c *Coordinator) RetrieveFile(ctx context.Context, req *protocol.RetrieveFileRequest) (*protocol.RetrieveFileResponse, error) {
	c.logger.Info("Retrieving file", zap.String("file_id", req.FileId))

	fileID := types.FileID(req.FileId)

	// Get file metadata
	c.fileMutex.RLock()
	file, exists := c.files[fileID]
	c.fileMutex.RUnlock()

	if !exists {
		return &protocol.RetrieveFileResponse{Success: false}, fmt.Errorf("file not found")
	}

	// Group chunks by index
	chunksByIndex := make(map[int][]types.ChunkLocation)
	for _, loc := range file.Chunks {
		chunksByIndex[loc.Index] = append(chunksByIndex[loc.Index], loc)
	}

	// Retrieve chunks
	chunks := make([]types.Chunk, len(chunksByIndex))
	for index, locations := range chunksByIndex {
		var chunkData []byte
		retrieved := false

		// Try to retrieve from any available replica
		for _, loc := range locations {
			c.nodeMutex.RLock()
			node, exists := c.nodes[loc.NodeID]
			c.nodeMutex.RUnlock()

			if !exists || !node.IsHealthy {
				continue
			}

			data, err := c.retrieveChunkFromNode(ctx, node, loc.ChunkID)
			if err != nil {
				c.logger.Warn("Failed to retrieve chunk from node",
					zap.String("chunk_id", string(loc.ChunkID)),
					zap.String("node_id", string(loc.NodeID)),
					zap.Error(err))
				continue
			}

			chunkData = data
			retrieved = true
			break
		}

		if !retrieved {
			return &protocol.RetrieveFileResponse{Success: false}, fmt.Errorf("failed to retrieve chunk %d", index)
		}

		chunks[index] = types.Chunk{
			ID:     locations[0].ChunkID,
			FileID: fileID,
			Index:  index,
			Size:   int64(len(chunkData)),
			Data:   chunkData,
		}
	}

	// Reassemble chunks
	cm := storage.NewChunkManager()
	data, err := cm.ReassembleChunks(chunks)
	if err != nil {
		return &protocol.RetrieveFileResponse{Success: false}, err
	}

	c.logger.Info("File retrieved successfully",
		zap.String("file_id", req.FileId),
		zap.Int("size", len(data)))

	return &protocol.RetrieveFileResponse{
		Success: true,
		Data:    data,
		Metadata: &protocol.FileMetadata{
			FileId:        string(file.ID),
			Filename:      file.Name,
			Size:          file.Size,
			CreatedAt:     file.CreatedAt.Unix(),
			OwnerMemberId: string(file.OwnerID),
		},
	}, nil
}

func (c *Coordinator) ShareNodeList(ctx context.Context, req *protocol.ShareNodeListRequest) (*protocol.ShareNodeListResponse, error) {
	for _, nodeInfo := range req.Nodes {
		c.addRemoteNode(nodeInfo)
	}
	return &protocol.ShareNodeListResponse{Success: true}, nil
}

func (c *Coordinator) UpdateMetadata(ctx context.Context, req *protocol.UpdateMetadataRequest) (*protocol.UpdateMetadataResponse, error) {
	return &protocol.UpdateMetadataResponse{Success: false}, fmt.Errorf("not implemented yet")
}

// GetStatus returns detailed status information about the coordinator
func (c *Coordinator) GetStatus(ctx context.Context, req *protocol.GetStatusRequest) (*protocol.GetStatusResponse, error) {
	// Collect all nodes (local and remote)
	c.nodeMutex.RLock()
	localNodes := make([]*protocol.NodeInfo, 0)
	remoteNodes := make([]*protocol.NodeInfo, 0)
	totalCapacity := int64(0)
	usedCapacity := int64(0)

	for _, node := range c.nodes {
		nodeInfo := &protocol.NodeInfo{
			NodeId:        string(node.ID),
			MemberId:      string(node.MemberID),
			Address:       node.Address,
			TotalCapacity: node.TotalCapacity,
			UsedCapacity:  node.UsedCapacity,
			IsHealthy:     node.IsHealthy,
		}

		if node.MemberID == c.memberID {
			localNodes = append(localNodes, nodeInfo)
			totalCapacity += node.TotalCapacity
			usedCapacity += node.UsedCapacity
		} else {
			remoteNodes = append(remoteNodes, nodeInfo)
		}
	}
	c.nodeMutex.RUnlock()

	// Sort nodes for consistent ordering
	sort.Slice(localNodes, func(i, j int) bool {
		return localNodes[i].NodeId < localNodes[j].NodeId
	})
	sort.Slice(remoteNodes, func(i, j int) bool {
		return remoteNodes[i].NodeId < remoteNodes[j].NodeId
	})

	// Collect peer info
	c.peerMutex.RLock()
	peerInfos := make([]*protocol.PeerInfo, 0, len(c.peers))
	for _, peer := range c.peers {
		peerInfos = append(peerInfos, &protocol.PeerInfo{
			MemberId:  string(peer.MemberID),
			Address:   peer.Address,
			IsHealthy: peer.IsHealthy,
			LastSeen:  peer.LastSeen.Unix(),
		})
	}
	c.peerMutex.RUnlock()

	// Sort peers for consistent ordering
	sort.Slice(peerInfos, func(i, j int) bool {
		return peerInfos[i].MemberId < peerInfos[j].MemberId
	})

	// Count files
	c.fileMutex.RLock()
	fileCount := len(c.files)
	c.fileMutex.RUnlock()

	return &protocol.GetStatusResponse{
		MemberId:             string(c.memberID),
		LocalNodes:           localNodes,
		RemoteNodes:          remoteNodes,
		Peers:                peerInfos,
		TotalFiles:           int32(fileCount),
		TotalStorageCapacity: totalCapacity,
		UsedStorageCapacity:  usedCapacity,
	}, nil
}

// RegisterNode implements the gRPC endpoint for node registration
func (c *Coordinator) RegisterNode(ctx context.Context, req *protocol.RegisterNodeRequest) (*protocol.RegisterNodeResponse, error) {
	// Only allow nodes from the same member
	if req.MemberId != string(c.memberID) {
		return &protocol.RegisterNodeResponse{
			Success: false,
			Message: "node must belong to the same member as coordinator",
		}, nil
	}

	c.registerNodeInternal(req.NodeId, req.Address, req.TotalCapacity)

	return &protocol.RegisterNodeResponse{
		Success: true,
		Message: "node registered successfully",
	}, nil
}

// registerNodeInternal handles the actual node registration
func (c *Coordinator) registerNodeInternal(nodeID, address string, capacity int64) {
	node := &types.StorageNode{
		ID:              types.NodeID(nodeID),
		MemberID:        c.memberID,
		Address:         address,
		TotalCapacity:   capacity,
		UsedCapacity:    0,
		IsHealthy:       true,
		LastHealthCheck: time.Now(),
	}

	c.nodeMutex.Lock()
	c.nodes[node.ID] = node
	c.nodeMutex.Unlock()

	c.logger.Info("Node registered",
		zap.String("node_id", nodeID),
		zap.String("address", address),
		zap.Int64("capacity", capacity))

	// Share updated node list with peers
	c.shareNodeListWithPeers()
}

func (c *Coordinator) shareNodeListWithPeers() {
	c.nodeMutex.RLock()
	nodeInfos := make([]*protocol.NodeInfo, 0)
	for _, node := range c.nodes {
		if node.MemberID == c.memberID {
			nodeInfos = append(nodeInfos, &protocol.NodeInfo{
				NodeId:        string(node.ID),
				MemberId:      string(node.MemberID),
				Address:       node.Address,
				TotalCapacity: node.TotalCapacity,
				UsedCapacity:  node.UsedCapacity,
				IsHealthy:     node.IsHealthy,
			})
		}
	}
	c.nodeMutex.RUnlock()

	if len(nodeInfos) == 0 {
		return
	}

	c.peerMutex.RLock()
	peers := make([]*Peer, 0, len(c.peers))
	for _, peer := range c.peers {
		peers = append(peers, peer)
	}
	c.peerMutex.RUnlock()

	for _, peer := range peers {
		go func(p *Peer) {
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			defer cancel()

			_, err := p.Client.ShareNodeList(ctx, &protocol.ShareNodeListRequest{
				MemberId: string(c.memberID),
				Nodes:    nodeInfos,
			})

			if err != nil {
				c.logger.Warn("Failed to share node list with peer",
					zap.String("peer", string(p.MemberID)),
					zap.Error(err))
			}
		}(peer)
	}
}

// SyncState handles state synchronization requests from peers
func (c *Coordinator) SyncState(ctx context.Context, req *protocol.SyncStateRequest) (*protocol.SyncStateResponse, error) {
	c.logger.Debug("Received state sync request", zap.String("from", req.MemberId))

	// Update our knowledge with the received state
	for _, nodeInfo := range req.Nodes {
		if types.MemberID(nodeInfo.MemberId) != c.memberID {
			c.addRemoteNode(nodeInfo)
		}
	}

	// Collect all nodes we know about
	c.nodeMutex.RLock()
	allNodes := make([]*protocol.NodeInfo, 0, len(c.nodes))
	for _, node := range c.nodes {
		allNodes = append(allNodes, &protocol.NodeInfo{
			NodeId:        string(node.ID),
			MemberId:      string(node.MemberID),
			Address:       node.Address,
			TotalCapacity: node.TotalCapacity,
			UsedCapacity:  node.UsedCapacity,
			IsHealthy:     node.IsHealthy,
		})
	}
	c.nodeMutex.RUnlock()

	// Sort nodes for consistent ordering
	sort.Slice(allNodes, func(i, j int) bool {
		return allNodes[i].NodeId < allNodes[j].NodeId
	})

	// Collect all peers we know about
	c.peerMutex.RLock()
	knownPeers := make([]*protocol.PeerInfo, 0, len(c.peers))
	for _, peer := range c.peers {
		knownPeers = append(knownPeers, &protocol.PeerInfo{
			MemberId:  string(peer.MemberID),
			Address:   peer.Address,
			IsHealthy: peer.IsHealthy,
			LastSeen:  peer.LastSeen.Unix(),
		})
	}
	c.peerMutex.RUnlock()

	// Sort peers for consistent ordering
	sort.Slice(knownPeers, func(i, j int) bool {
		return knownPeers[i].MemberId < knownPeers[j].MemberId
	})

	return &protocol.SyncStateResponse{
		Success:    true,
		Nodes:      allNodes,
		KnownPeers: knownPeers,
	}, nil
}

// syncLoop periodically syncs state with all connected peers
func (c *Coordinator) syncLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.syncWithPeers()
		case <-c.ctx.Done():
			return
		}
	}
}

// syncWithPeers sends sync requests to all connected peers
func (c *Coordinator) syncWithPeers() {
	// Collect our current state
	c.nodeMutex.RLock()
	myNodes := make([]*protocol.NodeInfo, 0)
	for _, node := range c.nodes {
		myNodes = append(myNodes, &protocol.NodeInfo{
			NodeId:        string(node.ID),
			MemberId:      string(node.MemberID),
			Address:       node.Address,
			TotalCapacity: node.TotalCapacity,
			UsedCapacity:  node.UsedCapacity,
			IsHealthy:     node.IsHealthy,
		})
	}
	c.nodeMutex.RUnlock()

	// Get list of peers to sync with
	c.peerMutex.RLock()
	peers := make([]*Peer, 0, len(c.peers))
	myPeers := make([]*protocol.PeerInfo, 0)
	for _, p := range c.peers {
		peers = append(peers, p)
		myPeers = append(myPeers, &protocol.PeerInfo{
			MemberId:  string(p.MemberID),
			Address:   p.Address,
			IsHealthy: p.IsHealthy,
			LastSeen:  p.LastSeen.Unix(),
		})
	}
	c.peerMutex.RUnlock()

	// Sync with each peer
	for _, peer := range peers {
		go func(p *Peer) {
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			defer cancel()

			resp, err := p.Client.SyncState(ctx, &protocol.SyncStateRequest{
				MemberId:   string(c.memberID),
				Timestamp:  time.Now().Unix(),
				Nodes:      myNodes,
				KnownPeers: myPeers,
			})

			if err != nil {
				c.logger.Warn("Failed to sync with peer",
					zap.String("peer", string(p.MemberID)),
					zap.Error(err))
				return
			}

			// Update our knowledge with the response
			for _, nodeInfo := range resp.Nodes {
				if types.MemberID(nodeInfo.MemberId) != c.memberID {
					c.addRemoteNode(nodeInfo)
				}
			}

			// Consider adding new peers we didn't know about
			for _, peerInfo := range resp.KnownPeers {
				peerID := types.MemberID(peerInfo.MemberId)
				if peerID != c.memberID {
					c.peerMutex.RLock()
					_, exists := c.peers[peerID]
					c.peerMutex.RUnlock()

					if !exists && peerInfo.Address != "" {
						c.logger.Info("Discovered new peer via sync",
							zap.String("peer", peerInfo.MemberId),
							zap.String("address", peerInfo.Address),
							zap.String("via", string(p.MemberID)))
						// Optionally connect to the new peer
						// go c.ConnectToPeer(peerInfo.MemberId, peerInfo.Address)
					}
				}
			}

			c.logger.Debug("Synced with peer",
				zap.String("peer", string(p.MemberID)),
				zap.Int("nodes_received", len(resp.Nodes)),
				zap.Int("peers_received", len(resp.KnownPeers)))
		}(peer)
	}
}

func (c *Coordinator) nodeHealthLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.checkNodeHealth()
		}
	}
}

func (c *Coordinator) checkNodeHealth() {
	c.nodeMutex.RLock()
	nodes := make([]*types.StorageNode, 0, len(c.nodes))
	for _, node := range c.nodes {
		if node.MemberID == c.memberID {
			nodes = append(nodes, node)
		}
	}
	c.nodeMutex.RUnlock()

	for _, node := range nodes {
		conn, err := c.connectToNode(node.Address)
		if err != nil {
			c.logger.Warn("Failed to connect to node for health check",
				zap.String("node_id", string(node.ID)),
				zap.Error(err))
			c.nodeMutex.Lock()
			if n, exists := c.nodes[node.ID]; exists {
				n.IsHealthy = false
			}
			c.nodeMutex.Unlock()
			continue
		}

		client := protocol.NewNodeClient(conn)
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		resp, err := client.HealthCheck(ctx, &protocol.HealthCheckRequest{
			Timestamp: time.Now().Unix(),
		})
		cancel()
		conn.Close()

		c.nodeMutex.Lock()
		if n, exists := c.nodes[node.ID]; exists {
			if err != nil || !resp.Healthy {
				n.IsHealthy = false
			} else {
				n.IsHealthy = true
				n.UsedCapacity = resp.UsedCapacity
				n.LastHealthCheck = time.Now()
			}
		}
		c.nodeMutex.Unlock()
	}
}

// connectToNode creates a gRPC connection to a storage node with proper TLS configuration
func (c *Coordinator) connectToNode(address string) (*grpc.ClientConn, error) {
	var dialOpts []grpc.DialOption

	// Use TLS if configured
	if c.authConfig != nil && c.authConfig.Enabled {
		tlsBuilder, err := auth.NewTLSConfigBuilder(c.authConfig)
		if err != nil {
			c.logger.Error("Failed to create TLS config builder", zap.Error(err))
			return nil, fmt.Errorf("failed to create TLS config builder: %w", err)
		}

		tlsConfig, err := tlsBuilder.BuildClientConfig()
		if err != nil {
			c.logger.Error("Failed to build client TLS config", zap.Error(err))
			return nil, fmt.Errorf("failed to build client TLS config: %w", err)
		}

		// Extract hostname from address for server name verification
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			// If no port, use the whole address as host
			host = address
		}
		tlsConfig.ServerName = host

		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
		c.logger.Debug("Connecting to node with TLS", zap.String("address", address))
	} else {
		// Only use insecure if TLS is explicitly disabled
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		c.logger.Debug("Connecting to node without TLS", zap.String("address", address))
	}

	return grpc.Dial(address, dialOpts...)
}

// Directory Operations
