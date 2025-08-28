package node

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"collective/pkg/auth"
	"collective/pkg/config"
	"collective/pkg/protocol"
	"collective/pkg/shared"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Node struct {
	protocol.UnimplementedNodeServer

	nodeID        types.NodeID
	memberID      types.MemberID
	address       string
	dataDir       string
	totalCapacity int64
	usedCapacity  int64
	logger        *zap.Logger
	authConfig    *auth.AuthConfig

	// Coordinator connection
	coordinatorAddress string
	coordinatorClient  protocol.CoordinatorClient
	coordinatorConn    *grpc.ClientConn

	// Chunk storage
	chunks      map[types.ChunkID]*types.Chunk
	chunksMutex sync.RWMutex

	server   *grpc.Server
	listener net.Listener

	ctx    context.Context
	cancel context.CancelFunc
}

func New(cfg *config.NodeConfig, memberID string, logger *zap.Logger) *Node {
	return NewWithAuth(cfg, memberID, logger, nil)
}

func NewWithAuth(cfg *config.NodeConfig, memberID string, logger *zap.Logger, authConfig *auth.AuthConfig) *Node {
	ctx, cancel := context.WithCancel(context.Background())

	return &Node{
		nodeID:             types.NodeID(cfg.NodeID),
		memberID:           types.MemberID(memberID),
		address:            cfg.Address,
		dataDir:            cfg.DataDir,
		totalCapacity:      cfg.StorageCapacity,
		usedCapacity:       0,
		logger:             logger,
		authConfig:         authConfig,
		coordinatorAddress: cfg.CoordinatorAddress,
		chunks:             make(map[types.ChunkID]*types.Chunk),
		ctx:                ctx,
		cancel:             cancel,
	}
}

func (n *Node) Start() error {
	// Create data directory
	if err := os.MkdirAll(n.dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Load existing chunks
	if err := n.loadExistingChunks(); err != nil {
		return fmt.Errorf("failed to load existing chunks: %w", err)
	}

	// Start gRPC server
	// Extract just the port from the address for binding
	bindAddr := n.address
	if host, port, err := net.SplitHostPort(n.address); err == nil && host != "" {
		// If there's a hostname, bind to all interfaces on the same port
		bindAddr = ":" + port
	}

	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", bindAddr, err)
	}

	n.listener = listener

	// Create gRPC server with authentication if configured
	var serverOpts []grpc.ServerOption

	if n.authConfig != nil && n.authConfig.Enabled {
		// Create TLS configuration
		tlsBuilder, err := auth.NewTLSConfigBuilder(n.authConfig)
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
			n.logger.Info("TLS enabled for node",
				zap.String("node_id", string(n.nodeID)),
				zap.String("member_id", string(n.memberID)))
		}
	}

	n.server = grpc.NewServer(serverOpts...)
	protocol.RegisterNodeServer(n.server, n)

	// Connect to coordinator
	if err := n.connectToCoordinator(); err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
	}

	// Register with coordinator
	if err := n.registerWithCoordinator(); err != nil {
		return fmt.Errorf("failed to register with coordinator: %w", err)
	}

	n.logger.Info("Node starting",
		zap.String("node_id", string(n.nodeID)),
		zap.String("address", n.address),
		zap.String("coordinator", n.coordinatorAddress))

	// Start background tasks
	go n.enhancedHealthReportLoop()     // Use enhanced version with heartbeat
	go n.MonitorCoordinatorConnection() // Monitor and reconnect if needed

	return n.server.Serve(listener)
}

func (n *Node) Stop() {
	n.cancel()

	// TODO: Add unregister RPC if needed

	if n.coordinatorConn != nil {
		n.coordinatorConn.Close()
	}

	if n.server != nil {
		n.server.GracefulStop()
	}
}

func (n *Node) connectToCoordinator() error {
	// Use shared connection utilities with auth config
	conn, err := shared.ConnectToCoordinatorWithAuth(n.coordinatorAddress, shared.DefaultGRPCTimeout, n.authConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
	}

	n.coordinatorConn = conn
	n.coordinatorClient = protocol.NewCoordinatorClient(conn)

	if n.authConfig != nil && n.authConfig.Enabled {
		n.logger.Info("Connected to coordinator with TLS",
			zap.String("coordinator", n.coordinatorAddress))
	}

	return nil
}

func (n *Node) registerWithCoordinator() error {
	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()

	resp, err := n.coordinatorClient.RegisterNode(ctx, &protocol.RegisterNodeRequest{
		NodeId:        string(n.nodeID),
		MemberId:      string(n.memberID),
		Address:       n.address,
		TotalCapacity: n.totalCapacity,
	})

	if err != nil {
		return fmt.Errorf("registration failed: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("registration rejected by coordinator: %s", resp.Message)
	}

	n.logger.Info("Successfully registered with coordinator", zap.String("message", resp.Message))
	return nil
}

func (n *Node) Register(ctx context.Context, req *protocol.RegisterRequest) (*protocol.RegisterResponse, error) {
	return &protocol.RegisterResponse{Success: false}, fmt.Errorf("nodes cannot register other nodes")
}

func (n *Node) Unregister(ctx context.Context, req *protocol.UnregisterRequest) (*protocol.UnregisterResponse, error) {
	return &protocol.UnregisterResponse{Success: false}, fmt.Errorf("nodes cannot unregister other nodes")
}

func (n *Node) StoreChunk(ctx context.Context, req *protocol.StoreChunkRequest) (*protocol.StoreChunkResponse, error) {
	chunkID := types.ChunkID(req.ChunkId)

	// Check if we have capacity
	if n.usedCapacity+int64(len(req.Data)) > n.totalCapacity {
		return &protocol.StoreChunkResponse{Success: false}, fmt.Errorf("insufficient capacity")
	}

	// Create chunk
	chunk := &types.Chunk{
		ID:     chunkID,
		FileID: types.FileID(req.FileId),
		Index:  int(req.Index),
		Size:   int64(len(req.Data)),
		Data:   req.Data,
		Hash:   fmt.Sprintf("%x", sha256.Sum256(req.Data)),
	}

	// Store to disk
	chunksDir := filepath.Join(n.dataDir, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return &protocol.StoreChunkResponse{Success: false}, fmt.Errorf("failed to create chunks directory: %w", err)
	}
	chunkPath := filepath.Join(chunksDir, string(chunkID))
	if err := os.WriteFile(chunkPath, req.Data, 0644); err != nil {
		return &protocol.StoreChunkResponse{Success: false}, fmt.Errorf("failed to write chunk to disk: %w", err)
	}

	// Store in memory
	n.chunksMutex.Lock()
	n.chunks[chunkID] = chunk
	n.usedCapacity += chunk.Size
	n.chunksMutex.Unlock()

	n.logger.Debug("Stored chunk",
		zap.String("chunk_id", string(chunkID)),
		zap.String("file_id", req.FileId),
		zap.Int64("size", chunk.Size))

	return &protocol.StoreChunkResponse{
		Success: true,
		ChunkId: string(chunkID),
	}, nil
}

func (n *Node) RetrieveChunk(ctx context.Context, req *protocol.RetrieveChunkRequest) (*protocol.RetrieveChunkResponse, error) {
	chunkID := types.ChunkID(req.ChunkId)

	n.chunksMutex.RLock()
	chunk, exists := n.chunks[chunkID]
	n.chunksMutex.RUnlock()

	if !exists {
		return &protocol.RetrieveChunkResponse{Success: false}, fmt.Errorf("chunk not found")
	}

	// Read from disk if data not in memory
	var data []byte
	if chunk.Data == nil {
		chunkPath := filepath.Join(n.dataDir, string(chunkID))
		var err error
		data, err = os.ReadFile(chunkPath)
		if err != nil {
			return &protocol.RetrieveChunkResponse{Success: false}, fmt.Errorf("failed to read chunk from disk: %w", err)
		}
	} else {
		data = chunk.Data
	}

	return &protocol.RetrieveChunkResponse{
		Success: true,
		Data:    data,
	}, nil
}

func (n *Node) DeleteChunk(ctx context.Context, req *protocol.DeleteChunkRequest) (*protocol.DeleteChunkResponse, error) {
	chunkID := types.ChunkID(req.ChunkId)

	n.chunksMutex.Lock()
	chunk, exists := n.chunks[chunkID]
	if exists {
		delete(n.chunks, chunkID)
		n.usedCapacity -= chunk.Size
	}
	n.chunksMutex.Unlock()

	if !exists {
		return &protocol.DeleteChunkResponse{Success: false}, fmt.Errorf("chunk not found")
	}

	// Delete from disk
	chunkPath := filepath.Join(n.dataDir, "chunks", string(chunkID))
	if err := os.Remove(chunkPath); err != nil && !os.IsNotExist(err) {
		n.logger.Warn("Failed to delete chunk file", zap.String("chunk_id", string(chunkID)), zap.Error(err))
	}

	return &protocol.DeleteChunkResponse{Success: true}, nil
}

func (n *Node) HealthCheck(ctx context.Context, req *protocol.HealthCheckRequest) (*protocol.HealthCheckResponse, error) {
	return &protocol.HealthCheckResponse{
		Healthy:       true,
		Timestamp:     time.Now().Unix(),
		UsedCapacity:  n.usedCapacity,
		TotalCapacity: n.totalCapacity,
	}, nil
}

func (n *Node) GetCapacity(ctx context.Context, req *protocol.GetCapacityRequest) (*protocol.GetCapacityResponse, error) {
	return &protocol.GetCapacityResponse{
		TotalCapacity:     n.totalCapacity,
		UsedCapacity:      n.usedCapacity,
		AvailableCapacity: n.totalCapacity - n.usedCapacity,
	}, nil
}

func (n *Node) TransferChunk(ctx context.Context, req *protocol.TransferChunkRequest) (*protocol.TransferChunkResponse, error) {
	return &protocol.TransferChunkResponse{Success: false}, fmt.Errorf("chunk transfer not implemented yet")
}

func (n *Node) loadExistingChunks() error {
	files, err := filepath.Glob(filepath.Join(n.dataDir, "*"))
	if err != nil {
		return err
	}

	for _, file := range files {
		chunkID := types.ChunkID(filepath.Base(file))
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		chunk := &types.Chunk{
			ID:   chunkID,
			Size: info.Size(),
		}

		n.chunks[chunkID] = chunk
		n.usedCapacity += chunk.Size
	}

	n.logger.Info("Loaded existing chunks",
		zap.Int("chunk_count", len(n.chunks)),
		zap.Int64("used_capacity", n.usedCapacity))

	return nil
}

func (n *Node) healthReportLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			// Health reporting to coordinator would go here
			n.logger.Debug("Health report",
				zap.Int64("used_capacity", n.usedCapacity),
				zap.Int64("total_capacity", n.totalCapacity))
		}
	}
}
