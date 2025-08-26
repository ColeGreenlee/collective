package storage

import (
	"context"
	"fmt"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
)

type ChunkTransfer struct {
	logger *zap.Logger
}

func NewChunkTransfer(logger *zap.Logger) *ChunkTransfer {
	return &ChunkTransfer{
		logger: logger,
	}
}

// TransferChunk moves a chunk from source to target node
func (ct *ChunkTransfer) TransferChunk(ctx context.Context, chunkID types.ChunkID, sourceNode, targetNode *types.StorageNode) error {
	ct.logger.Info("Starting chunk transfer",
		zap.String("chunk_id", string(chunkID)),
		zap.String("source", string(sourceNode.ID)),
		zap.String("target", string(targetNode.ID)))

	// Connect to source node
	sourceConn, err := ct.connectToNode(sourceNode.Address)
	if err != nil {
		return fmt.Errorf("failed to connect to source node %s: %w", sourceNode.ID, err)
	}
	defer sourceConn.Close()

	sourceClient := protocol.NewNodeClient(sourceConn)

	// Retrieve chunk from source
	retrieveResp, err := sourceClient.RetrieveChunk(ctx, &protocol.RetrieveChunkRequest{
		ChunkId: string(chunkID),
	})
	if err != nil {
		return fmt.Errorf("failed to retrieve chunk from source: %w", err)
	}
	if !retrieveResp.Success {
		return fmt.Errorf("chunk retrieval failed at source")
	}

	// Connect to target node
	targetConn, err := ct.connectToNode(targetNode.Address)
	if err != nil {
		return fmt.Errorf("failed to connect to target node %s: %w", targetNode.ID, err)
	}
	defer targetConn.Close()

	targetClient := protocol.NewNodeClient(targetConn)

	// Store chunk on target
	storeResp, err := targetClient.StoreChunk(ctx, &protocol.StoreChunkRequest{
		ChunkId: string(chunkID),
		Data:    retrieveResp.Data,
	})
	if err != nil {
		return fmt.Errorf("failed to store chunk on target: %w", err)
	}
	if !storeResp.Success {
		return fmt.Errorf("chunk storage failed at target")
	}

	ct.logger.Info("Chunk transfer completed",
		zap.String("chunk_id", string(chunkID)),
		zap.String("source", string(sourceNode.ID)),
		zap.String("target", string(targetNode.ID)))

	return nil
}

// ParallelTransfer moves multiple chunks in parallel
func (ct *ChunkTransfer) ParallelTransfer(ctx context.Context, transfers map[types.ChunkID]NodePair, maxConcurrency int) error {
	if maxConcurrency <= 0 {
		maxConcurrency = 5
	}

	sem := make(chan struct{}, maxConcurrency)
	errChan := make(chan error, len(transfers))

	for chunkID, pair := range transfers {
		sem <- struct{}{} // Acquire semaphore
		
		go func(cID types.ChunkID, p NodePair) {
			defer func() { <-sem }() // Release semaphore
			
			if err := ct.TransferChunk(ctx, cID, p.Source, p.Target); err != nil {
				errChan <- fmt.Errorf("transfer failed for chunk %s: %w", cID, err)
			}
		}(chunkID, pair)
	}

	// Wait for all transfers to complete
	for i := 0; i < maxConcurrency; i++ {
		sem <- struct{}{}
	}

	close(errChan)

	// Collect errors
	var firstErr error
	errorCount := 0
	for err := range errChan {
		if firstErr == nil {
			firstErr = err
		}
		errorCount++
		ct.logger.Error("Transfer error", zap.Error(err))
	}

	if errorCount > 0 {
		return fmt.Errorf("%d transfers failed, first error: %w", errorCount, firstErr)
	}

	return nil
}

// ReplicateChunk creates additional copies of a chunk on new nodes
func (ct *ChunkTransfer) ReplicateChunk(ctx context.Context, chunkID types.ChunkID, sourceNode *types.StorageNode, targetNodes []*types.StorageNode) error {
	// Connect to source node
	sourceConn, err := ct.connectToNode(sourceNode.Address)
	if err != nil {
		return fmt.Errorf("failed to connect to source node: %w", err)
	}
	defer sourceConn.Close()

	sourceClient := protocol.NewNodeClient(sourceConn)

	// Retrieve chunk once from source
	retrieveResp, err := sourceClient.RetrieveChunk(ctx, &protocol.RetrieveChunkRequest{
		ChunkId: string(chunkID),
	})
	if err != nil {
		return fmt.Errorf("failed to retrieve chunk: %w", err)
	}
	if !retrieveResp.Success {
		return fmt.Errorf("chunk retrieval failed")
	}

	// Store on all target nodes
	errors := []error{}
	for _, targetNode := range targetNodes {
		if err := ct.storeChunkOnNode(ctx, chunkID, retrieveResp.Data, targetNode); err != nil {
			errors = append(errors, fmt.Errorf("failed to replicate to %s: %w", targetNode.ID, err))
		} else {
			ct.logger.Info("Chunk replicated",
				zap.String("chunk_id", string(chunkID)),
				zap.String("target", string(targetNode.ID)))
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("replication partially failed: %v", errors)
	}

	return nil
}

func (ct *ChunkTransfer) storeChunkOnNode(ctx context.Context, chunkID types.ChunkID, data []byte, node *types.StorageNode) error {
	conn, err := ct.connectToNode(node.Address)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := protocol.NewNodeClient(conn)
	resp, err := client.StoreChunk(ctx, &protocol.StoreChunkRequest{
		ChunkId: string(chunkID),
		Data:    data,
	})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("store operation failed")
	}

	return nil
}

func (ct *ChunkTransfer) connectToNode(address string) (*grpc.ClientConn, error) {
	backoffConfig := backoff.Config{
		BaseDelay:  1 * time.Second,
		Multiplier: 1.5,
		Jitter:     0.2,
		MaxDelay:   30 * time.Second,
	}

	return grpc.Dial(address,
		grpc.WithInsecure(),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoffConfig,
			MinConnectTimeout: 5 * time.Second,
		}),
	)
}

type NodePair struct {
	Source *types.StorageNode
	Target *types.StorageNode
}