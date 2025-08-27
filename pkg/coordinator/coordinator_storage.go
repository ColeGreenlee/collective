package coordinator

import (
	"context"
	"fmt"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/storage"
	"collective/pkg/types"

	"go.uber.org/zap"
)

// storeChunkOnNode stores a chunk on a specific node (extracted for reuse)
func (c *Coordinator) storeChunkOnNode(ctx context.Context, node *types.StorageNode, chunk types.Chunk) error {
	conn, err := c.getNodeConnection(node.Address)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	client := protocol.NewNodeClient(conn)
	storeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := client.StoreChunk(storeCtx, &protocol.StoreChunkRequest{
		ChunkId: string(chunk.ID),
		FileId:  string(chunk.FileID),
		Index:   int32(chunk.Index),
		Data:    chunk.Data,
	})

	if err != nil {
		return err
	}

	if !resp.Success {
		return fmt.Errorf("node rejected chunk storage")
	}

	return nil
}

// retrieveChunksFromNodes retrieves chunks from storage nodes
func (c *Coordinator) retrieveChunksFromNodes(ctx context.Context, chunkIDs []types.ChunkID) ([]types.Chunk, error) {
	c.chunkMutex.RLock()
	allocations := c.chunkAllocations
	c.chunkMutex.RUnlock()

	var chunks []types.Chunk
	for _, chunkID := range chunkIDs {
		nodeIDs := allocations[chunkID]
		if len(nodeIDs) == 0 {
			return nil, fmt.Errorf("no nodes found for chunk %s", chunkID)
		}

		// Try to retrieve from first available node
		var retrievedChunk *types.Chunk
		for _, nodeID := range nodeIDs {
			c.nodeMutex.RLock()
			node := c.nodes[nodeID]
			c.nodeMutex.RUnlock()

			if node == nil || !node.IsHealthy {
				continue
			}

			conn, err := c.getNodeConnection(node.Address)
			if err != nil {
				c.logger.Warn("Failed to connect to node",
					zap.String("node", string(nodeID)),
					zap.Error(err))
				continue
			}

			client := protocol.NewNodeClient(conn)
			retrieveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

			resp, err := client.RetrieveChunk(retrieveCtx, &protocol.RetrieveChunkRequest{
				ChunkId: string(chunkID),
			})

			cancel()

			if err != nil || !resp.Success {
				c.logger.Warn("Failed to retrieve chunk from node",
					zap.String("node", string(nodeID)),
					zap.String("chunk", string(chunkID)),
					zap.Error(err))
				continue
			}

			retrievedChunk = &types.Chunk{
				ID:     chunkID,
				FileID: types.FileID(""), // FileID not in response
				Index:  0,                // Index not in response
				Data:   resp.Data,
			}
			break
		}

		if retrievedChunk == nil {
			return nil, fmt.Errorf("failed to retrieve chunk %s from any node", chunkID)
		}

		chunks = append(chunks, *retrievedChunk)
	}

	return chunks, nil
}

// allocateChunksToNodes allocates chunks to storage nodes using distribution strategy
func (c *Coordinator) allocateChunksToNodes(chunks []types.Chunk, replicationFactor int) (map[types.ChunkID][]types.NodeID, error) {
	// Get healthy nodes for chunk storage
	c.nodeMutex.RLock()
	nodeList := []*types.StorageNode{}
	for _, node := range c.nodes {
		if node.IsHealthy {
			nodeList = append(nodeList, node)
		}
	}
	c.nodeMutex.RUnlock()

	if len(nodeList) == 0 {
		return nil, fmt.Errorf("no healthy storage nodes available")
	}

	// Allocate chunks to nodes
	distStrategy := storage.NewDistributionStrategy(replicationFactor)
	allocations, err := distStrategy.AllocateChunks(chunks, nodeList)
	if err != nil {
		return nil, fmt.Errorf("failed to allocate chunks: %w", err)
	}

	return allocations, nil
}
