package coordinator

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"collective/pkg/config"
	"collective/pkg/protocol"
	"collective/pkg/storage"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type Coordinator struct {
	protocol.UnimplementedCoordinatorServer
	
	memberID types.MemberID
	address  string
	logger   *zap.Logger
	config   *config.CoordinatorConfig
	
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
	
	server   *grpc.Server
	listener net.Listener
	
	ctx    context.Context
	cancel context.CancelFunc
}

type Peer struct {
	MemberID   types.MemberID
	Address    string
	Client     protocol.CoordinatorClient
	Connection *grpc.ClientConn
	LastSeen   time.Time
	IsHealthy  bool
}

func New(cfg *config.CoordinatorConfig, memberID string, logger *zap.Logger) *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	
	return &Coordinator{
		memberID: types.MemberID(memberID),
		address:  cfg.Address,
		logger:   logger,
		config:   cfg,
		peers:       make(map[types.MemberID]*Peer),
		nodes:       make(map[types.NodeID]*types.StorageNode),
		files:       make(map[types.FileID]*types.File),
		directories: make(map[string]*types.Directory),
		fileEntries: make(map[string]*types.FileEntry),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (c *Coordinator) Start() error {
	listener, err := net.Listen("tcp", c.address)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", c.address, err)
	}
	
	c.listener = listener
	c.server = grpc.NewServer()
	protocol.RegisterCoordinatorServer(c.server, c)
	
	c.logger.Info("Coordinator starting", zap.String("address", c.address), zap.String("member_id", string(c.memberID)))
	
	// Initialize root directory
	c.initializeRootDirectory()
	
	// Start background tasks
	go c.heartbeatLoop()
	go c.syncLoop()
	go c.nodeHealthLoop()
	
	return c.server.Serve(listener)
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
	
	conn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("failed to connect to peer %s at %s: %w", memberID, address, err)
	}
	
	client := protocol.NewCoordinatorClient(conn)
	
	// Get our node list to share
	c.nodeMutex.RLock()
	nodeInfos := make([]*protocol.NodeInfo, 0, len(c.nodes))
	for _, node := range c.nodes {
		nodeInfos = append(nodeInfos, &protocol.NodeInfo{
			NodeId:       string(node.ID),
			MemberId:     string(node.MemberID),
			Address:      node.Address,
			TotalCapacity: node.TotalCapacity,
			UsedCapacity:  node.UsedCapacity,
			IsHealthy:    node.IsHealthy,
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
		conn, err := grpc.Dial(req.Address, grpc.WithInsecure())
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
				NodeId:       string(node.ID),
				MemberId:     string(node.MemberID),
				Address:      node.Address,
				TotalCapacity: node.TotalCapacity,
				UsedCapacity:  node.UsedCapacity,
				IsHealthy:    node.IsHealthy,
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
	
	c.peerMutex.Lock()
	if peer, exists := c.peers[memberID]; exists {
		peer.LastSeen = time.Now()
		peer.IsHealthy = true
	}
	c.peerMutex.Unlock()
	
	return &protocol.HeartbeatResponse{
		MemberId:  string(c.memberID),
		Timestamp: time.Now().Unix(),
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
			if err := c.storeChunkOnNode(ctx, &chunk, targetNode); err != nil {
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

			data, err := c.retrieveChunkFromNode(ctx, loc.ChunkID, node)
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
			FileId:       string(file.ID),
			Filename:     file.Name,
			Size:         file.Size,
			CreatedAt:    file.CreatedAt.Unix(),
			OwnerMemberId: string(file.OwnerID),
		},
	}, nil
}

func (c *Coordinator) storeChunkOnNode(ctx context.Context, chunk *types.Chunk, node *types.StorageNode) error {
	conn, err := grpc.Dial(node.Address, grpc.WithInsecure())
	if err != nil {
		return fmt.Errorf("failed to connect to node: %w", err)
	}
	defer conn.Close()

	client := protocol.NewNodeClient(conn)
	resp, err := client.StoreChunk(ctx, &protocol.StoreChunkRequest{
		ChunkId: string(chunk.ID),
		Data:    chunk.Data,
		FileId:  string(chunk.FileID),
		Index:   int32(chunk.Index),
	})

	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("node rejected chunk storage")
	}

	return nil
}

func (c *Coordinator) retrieveChunkFromNode(ctx context.Context, chunkID types.ChunkID, node *types.StorageNode) ([]byte, error) {
	conn, err := grpc.Dial(node.Address, grpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to node: %w", err)
	}
	defer conn.Close()

	client := protocol.NewNodeClient(conn)
	resp, err := client.RetrieveChunk(ctx, &protocol.RetrieveChunkRequest{
		ChunkId: string(chunkID),
	})

	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("chunk not found on node")
	}

	return resp.Data, nil
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
		MemberId:            string(c.memberID),
		LocalNodes:          localNodes,
		RemoteNodes:         remoteNodes,
		Peers:               peerInfos,
		TotalFiles:          int32(fileCount),
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
		conn, err := grpc.Dial(node.Address, grpc.WithInsecure())
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

// Directory Operations

func (c *Coordinator) CreateDirectory(ctx context.Context, req *protocol.CreateDirectoryRequest) (*protocol.CreateDirectoryResponse, error) {
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	path := req.Path
	if path == "" {
		return &protocol.CreateDirectoryResponse{
			Success: false,
			Message: "path cannot be empty",
		}, nil
	}
	
	// Check if directory already exists
	if _, exists := c.directories[path]; exists {
		return &protocol.CreateDirectoryResponse{
			Success: false,
			Message: "directory already exists",
		}, nil
	}
	
	// Create parent directories if they don't exist
	parent := getParentPath(path)
	if parent != "/" && parent != "" {
		if _, exists := c.directories[parent]; !exists {
			// Release lock before recursive call to avoid deadlock
			c.directoryMutex.Unlock()
			
			// Create parent directory recursively
			_, err := c.CreateDirectory(ctx, &protocol.CreateDirectoryRequest{
				Path: parent,
				Mode: req.Mode,
			})
			
			// Reacquire lock
			c.directoryMutex.Lock()
			
			if err != nil {
				return &protocol.CreateDirectoryResponse{
					Success: false,
					Message: fmt.Sprintf("failed to create parent directory: %v", err),
				}, nil
			}
		}
	}
	
	// Create the directory
	dir := &types.Directory{
		Path:     path,
		Parent:   parent,
		Children: []string{},
		Mode:     os.FileMode(req.Mode),
		Modified: time.Now(),
		Owner:    c.memberID,
	}
	
	c.directories[path] = dir
	
	// Add to parent's children list
	if parent != "" {
		if parentDir, exists := c.directories[parent]; exists {
			parentDir.Children = append(parentDir.Children, path)
		}
	}
	
	c.logger.Info("Created directory", 
		zap.String("path", path),
		zap.String("owner", string(c.memberID)))
	
	return &protocol.CreateDirectoryResponse{
		Success: true,
		Message: "directory created successfully",
	}, nil
}

func (c *Coordinator) ListDirectory(ctx context.Context, req *protocol.ListDirectoryRequest) (*protocol.ListDirectoryResponse, error) {
	c.directoryMutex.RLock()
	defer c.directoryMutex.RUnlock()
	
	path := req.Path
	if path == "" {
		path = "/"
	}
	
	var entries []*protocol.DirectoryEntry
	
	// Check if it's a directory
	if dir, exists := c.directories[path]; exists {
		for _, childPath := range dir.Children {
			if childDir, exists := c.directories[childPath]; exists {
				entries = append(entries, &protocol.DirectoryEntry{
					Name:         getBaseName(childPath),
					Path:         childPath,
					IsDirectory:  true,
					Size:         0,
					Mode:         uint32(childDir.Mode),
					ModifiedTime: childDir.Modified.Unix(),
					Owner:        string(childDir.Owner),
				})
			} else if fileEntry, exists := c.fileEntries[childPath]; exists {
				entries = append(entries, &protocol.DirectoryEntry{
					Name:         getBaseName(childPath),
					Path:         childPath,
					IsDirectory:  false,
					Size:         fileEntry.Size,
					Mode:         uint32(fileEntry.Mode),
					ModifiedTime: fileEntry.Modified.Unix(),
					Owner:        string(fileEntry.Owner),
				})
			}
		}
	} else {
		return &protocol.ListDirectoryResponse{
			Success: false,
			Entries: nil,
		}, nil
	}
	
	return &protocol.ListDirectoryResponse{
		Success: true,
		Entries: entries,
	}, nil
}

func (c *Coordinator) DeleteDirectory(ctx context.Context, req *protocol.DeleteDirectoryRequest) (*protocol.DeleteDirectoryResponse, error) {
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	path := req.Path
	if path == "" || path == "/" {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "cannot delete root directory",
		}, nil
	}
	
	dir, exists := c.directories[path]
	if !exists {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "directory does not exist",
		}, nil
	}
	
	// Check if directory is empty (unless recursive flag is set)
	if len(dir.Children) > 0 && !req.Recursive {
		return &protocol.DeleteDirectoryResponse{
			Success: false,
			Message: "directory is not empty",
		}, nil
	}
	
	// If recursive, delete all children first
	if req.Recursive {
		for _, childPath := range dir.Children {
			if _, isDir := c.directories[childPath]; isDir {
				_, err := c.DeleteDirectory(ctx, &protocol.DeleteDirectoryRequest{
					Path:      childPath,
					Recursive: true,
				})
				if err != nil {
					return &protocol.DeleteDirectoryResponse{
						Success: false,
						Message: fmt.Sprintf("failed to delete child directory %s: %v", childPath, err),
					}, nil
				}
			} else {
				// Delete file entry
				delete(c.fileEntries, childPath)
			}
		}
	}
	
	// Remove from parent's children list
	if dir.Parent != "" && dir.Parent != "/" {
		if parent, exists := c.directories[dir.Parent]; exists {
			for i, child := range parent.Children {
				if child == path {
					parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
					break
				}
			}
		}
	}
	
	// Delete the directory
	delete(c.directories, path)
	
	c.logger.Info("Deleted directory", 
		zap.String("path", path),
		zap.Bool("recursive", req.Recursive))
	
	return &protocol.DeleteDirectoryResponse{
		Success: true,
		Message: "directory deleted successfully",
	}, nil
}

func (c *Coordinator) StatEntry(ctx context.Context, req *protocol.StatEntryRequest) (*protocol.StatEntryResponse, error) {
	c.directoryMutex.RLock()
	defer c.directoryMutex.RUnlock()
	
	path := req.Path
	
	// Check if it's a directory
	if dir, exists := c.directories[path]; exists {
		return &protocol.StatEntryResponse{
			Success: true,
			Entry: &protocol.DirectoryEntry{
				Name:         getBaseName(path),
				Path:         path,
				IsDirectory:  true,
				Size:         0,
				Mode:         uint32(dir.Mode),
				ModifiedTime: dir.Modified.Unix(),
				Owner:        string(dir.Owner),
			},
		}, nil
	}
	
	// Check if it's a file
	if fileEntry, exists := c.fileEntries[path]; exists {
		return &protocol.StatEntryResponse{
			Success: true,
			Entry: &protocol.DirectoryEntry{
				Name:         getBaseName(path),
				Path:         path,
				IsDirectory:  false,
				Size:         fileEntry.Size,
				Mode:         uint32(fileEntry.Mode),
				ModifiedTime: fileEntry.Modified.Unix(),
				Owner:        string(fileEntry.Owner),
			},
		}, nil
	}
	
	return &protocol.StatEntryResponse{
		Success: false,
		Entry:   nil,
	}, nil
}

func (c *Coordinator) MoveEntry(ctx context.Context, req *protocol.MoveEntryRequest) (*protocol.MoveEntryResponse, error) {
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	oldPath := req.OldPath
	newPath := req.NewPath
	
	// Check if source exists
	isDirectory := false
	if _, exists := c.directories[oldPath]; exists {
		isDirectory = true
	} else if _, exists := c.fileEntries[oldPath]; !exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "source path does not exist",
		}, nil
	}
	
	// Check if destination already exists
	if _, exists := c.directories[newPath]; exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "destination already exists",
		}, nil
	}
	if _, exists := c.fileEntries[newPath]; exists {
		return &protocol.MoveEntryResponse{
			Success: false,
			Message: "destination already exists",
		}, nil
	}
	
	// Ensure destination parent exists
	newParent := getParentPath(newPath)
	if newParent != "/" && newParent != "" {
		if _, exists := c.directories[newParent]; !exists {
			return &protocol.MoveEntryResponse{
				Success: false,
				Message: "destination parent directory does not exist",
			}, nil
		}
	}
	
	if isDirectory {
		// Move directory
		dir := c.directories[oldPath]
		
		// Update all children paths recursively
		c.updateChildrenPaths(oldPath, newPath)
		
		// Remove from old parent
		if dir.Parent != "" && dir.Parent != "/" {
			if parent, exists := c.directories[dir.Parent]; exists {
				for i, child := range parent.Children {
					if child == oldPath {
						parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
						break
					}
				}
			}
		}
		
		// Update directory path and parent
		dir.Path = newPath
		dir.Parent = newParent
		
		// Add to new parent
		if newParent != "" && newParent != "/" {
			if parent, exists := c.directories[newParent]; exists {
				parent.Children = append(parent.Children, newPath)
			}
		}
		
		// Update the map
		delete(c.directories, oldPath)
		c.directories[newPath] = dir
		
	} else {
		// Move file entry
		fileEntry := c.fileEntries[oldPath]
		
		// Remove from old parent
		oldParent := getParentPath(oldPath)
		if oldParent != "" && oldParent != "/" {
			if parent, exists := c.directories[oldParent]; exists {
				for i, child := range parent.Children {
					if child == oldPath {
						parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
						break
					}
				}
			}
		}
		
		// Update file entry path
		fileEntry.Path = newPath
		
		// Add to new parent
		if newParent != "" && newParent != "/" {
			if parent, exists := c.directories[newParent]; exists {
				parent.Children = append(parent.Children, newPath)
			}
		}
		
		// Update the map
		delete(c.fileEntries, oldPath)
		c.fileEntries[newPath] = fileEntry
	}
	
	c.logger.Info("Moved entry",
		zap.String("oldPath", oldPath),
		zap.String("newPath", newPath),
		zap.Bool("isDirectory", isDirectory))
	
	return &protocol.MoveEntryResponse{
		Success: true,
		Message: "entry moved successfully",
	}, nil
}

// Helper functions

func (c *Coordinator) initializeRootDirectory() {
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	// Create root directory if it doesn't exist
	if _, exists := c.directories["/"]; !exists {
		c.directories["/"] = &types.Directory{
			Path:     "/",
			Parent:   "",
			Children: []string{},
			Mode:     os.FileMode(0755),
			Modified: time.Now(),
			Owner:    c.memberID,
		}
		c.logger.Info("Initialized root directory")
	}
}

func getParentPath(path string) string {
	if path == "/" || path == "" {
		return ""
	}
	
	// Remove trailing slash if present
	if path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash <= 0 {
		return "/"
	}
	return path[:lastSlash]
}

func getBaseName(path string) string {
	if path == "/" || path == "" {
		return ""
	}
	
	// Remove trailing slash if present
	if path[len(path)-1] == '/' {
		path = path[:len(path)-1]
	}
	
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		return path
	}
	return path[lastSlash+1:]
}

func (c *Coordinator) updateChildrenPaths(oldPrefix, newPrefix string) {
	// Update all directories that start with the old prefix
	for path, dir := range c.directories {
		if strings.HasPrefix(path, oldPrefix+"/") {
			newPath := newPrefix + path[len(oldPrefix):]
			dir.Path = newPath
			dir.Parent = getParentPath(newPath)
			
			delete(c.directories, path)
			c.directories[newPath] = dir
		}
	}
	
	// Update all file entries that start with the old prefix
	for path, fileEntry := range c.fileEntries {
		if strings.HasPrefix(path, oldPrefix+"/") {
			newPath := newPrefix + path[len(oldPrefix):]
			fileEntry.Path = newPath
			
			delete(c.fileEntries, path)
			c.fileEntries[newPath] = fileEntry
		}
	}
}

// CreateFile creates a new file at the specified path
func (c *Coordinator) CreateFile(ctx context.Context, req *protocol.CreateFileRequest) (*protocol.CreateFileResponse, error) {
	c.logger.Debug("CreateFile request", zap.String("path", req.Path))
	
	// Validate path
	if req.Path == "" || req.Path == "/" {
		return &protocol.CreateFileResponse{
			Success: false,
			Message: "Invalid file path",
		}, nil
	}
	
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	// Check if file already exists
	if _, exists := c.fileEntries[req.Path]; exists {
		return &protocol.CreateFileResponse{
			Success: false,
			Message: "File already exists",
		}, nil
	}
	
	// Check if path conflicts with directory
	if _, exists := c.directories[req.Path]; exists {
		return &protocol.CreateFileResponse{
			Success: false,
			Message: "Path conflicts with existing directory",
		}, nil
	}
	
	// Ensure parent directory exists
	parent := getParentPath(req.Path)
	if parent != "" && parent != "/" {
		if _, exists := c.directories[parent]; !exists {
			return &protocol.CreateFileResponse{
				Success: false,
				Message: "Parent directory does not exist",
			}, nil
		}
	}
	
	// Create file entry
	fileEntry := &types.FileEntry{
		Path:     req.Path,
		Size:     0,
		Mode:     os.FileMode(req.Mode),
		Modified: time.Now(),
		ChunkIDs: []types.ChunkID{},
		Owner:    c.memberID,
	}
	
	c.fileEntries[req.Path] = fileEntry
	
	// Add to parent directory's children
	if parent != "" {
		if parentDir, exists := c.directories[parent]; exists {
			parentDir.Children = append(parentDir.Children, req.Path)
		}
	}
	
	c.logger.Info("Created file", zap.String("path", req.Path), zap.Uint32("mode", req.Mode))
	
	return &protocol.CreateFileResponse{
		Success: true,
		Message: "File created successfully",
	}, nil
}

// ReadFile reads data from a file
func (c *Coordinator) ReadFile(ctx context.Context, req *protocol.ReadFileRequest) (*protocol.ReadFileResponse, error) {
	c.logger.Debug("ReadFile request", 
		zap.String("path", req.Path),
		zap.Int64("offset", req.Offset),
		zap.Int64("length", req.Length))
	
	c.directoryMutex.RLock()
	fileEntry, exists := c.fileEntries[req.Path]
	c.directoryMutex.RUnlock()
	
	if !exists {
		return &protocol.ReadFileResponse{
			Success: false,
			Data:    nil,
		}, nil
	}
	
	// Handle empty file or read beyond file
	if fileEntry.Size == 0 || req.Offset >= fileEntry.Size {
		return &protocol.ReadFileResponse{
			Success:   true,
			Data:      []byte{},
			BytesRead: 0,
		}, nil
	}
	
	// For now, we'll implement a simple in-memory file storage
	// In a real implementation, this would read from chunks
	// TODO: Implement chunk-based file reading
	
	// Return empty data for now since files aren't actually stored in chunks yet
	return &protocol.ReadFileResponse{
		Success:   true,
		Data:      []byte{},
		BytesRead: 0,
	}, nil
}

// WriteFile writes data to a file
func (c *Coordinator) WriteFile(ctx context.Context, req *protocol.WriteFileRequest) (*protocol.WriteFileResponse, error) {
	c.logger.Debug("WriteFile request",
		zap.String("path", req.Path),
		zap.Int64("offset", req.Offset),
		zap.Int("data_size", len(req.Data)))
	
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	fileEntry, exists := c.fileEntries[req.Path]
	if !exists {
		return &protocol.WriteFileResponse{
			Success:      false,
			BytesWritten: 0,
			Message:      "File does not exist",
		}, nil
	}
	
	// For now, we'll simulate writing by updating the file size
	// In a real implementation, this would create/update chunks
	// TODO: Implement chunk-based file writing
	
	newSize := req.Offset + int64(len(req.Data))
	if newSize > fileEntry.Size {
		fileEntry.Size = newSize
	}
	fileEntry.Modified = time.Now()
	
	return &protocol.WriteFileResponse{
		Success:      true,
		BytesWritten: int64(len(req.Data)),
		Message:      "Data written successfully",
	}, nil
}

// DeleteFile deletes a file
func (c *Coordinator) DeleteFile(ctx context.Context, req *protocol.DeleteFileRequest) (*protocol.DeleteFileResponse, error) {
	c.logger.Debug("DeleteFile request", zap.String("path", req.Path))
	
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	// Check if file exists
	fileEntry, exists := c.fileEntries[req.Path]
	if !exists {
		return &protocol.DeleteFileResponse{
			Success: false,
			Message: "File does not exist",
		}, nil
	}
	
	// Remove from file entries
	delete(c.fileEntries, req.Path)
	
	// Remove from parent directory's children
	parent := getParentPath(req.Path)
	if parent != "" {
		if parentDir, exists := c.directories[parent]; exists {
			children := parentDir.Children
			for i, child := range children {
				if child == req.Path {
					parentDir.Children = append(children[:i], children[i+1:]...)
					break
				}
			}
		}
	}
	
	// TODO: Delete associated chunks
	c.logger.Info("Deleted file", zap.String("path", req.Path), zap.Int("chunks", len(fileEntry.ChunkIDs)))
	
	return &protocol.DeleteFileResponse{
		Success: true,
		Message: "File deleted successfully",
	}, nil
}