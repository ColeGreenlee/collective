package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/storage"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// Connection pool for node connections
var (
	nodeConnections = make(map[string]*grpc.ClientConn)
	nodeConnMutex   sync.RWMutex
)

// WriteFileStreamStandard handles streaming file uploads for large files (original implementation)
func (c *Coordinator) WriteFileStreamStandard(stream protocol.Coordinator_WriteFileStreamServer) error {
	var path string
	var totalSize int64
	var receivedData []byte
	headerReceived := false

	// Receive all chunks
	for {
		req, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return err
		}

		switch data := req.Data.(type) {
		case *protocol.WriteFileStreamRequest_Header:
			if headerReceived {
				return fmt.Errorf("header already received")
			}
			path = data.Header.Path
			totalSize = int64(len(data.Header.Path)) // Size not available in header
			headerReceived = true
			receivedData = make([]byte, 0)

			c.logger.Debug("Streaming file upload started",
				zap.String("path", path),
				zap.Int64("size", totalSize))

		case *protocol.WriteFileStreamRequest_ChunkData:
			if !headerReceived {
				return fmt.Errorf("header not received")
			}
			receivedData = append(receivedData, data.ChunkData...)
		}
	}

	if !headerReceived {
		return fmt.Errorf("no header received in stream")
	}

	c.logger.Info("Received streaming file upload",
		zap.String("path", path),
		zap.Int("received_bytes", len(receivedData)))

	// Now process the complete file data using parallel chunk operations
	return c.writeFileWithParallelChunks(stream.Context(), path, receivedData, stream)
}

// writeFileWithParallelChunks processes file data with parallel chunk storage
func (c *Coordinator) writeFileWithParallelChunks(ctx context.Context, path string, data []byte, stream protocol.Coordinator_WriteFileStreamServer) error {
	// Get file lock for this specific file
	fileLock := c.getFileLock(path)
	fileLock.Lock()
	defer fileLock.Unlock()

	// Quick check if file exists (minimal lock time)
	c.directoryMutex.RLock()
	_, exists := c.fileEntries[path]
	if !exists {
		c.directoryMutex.RUnlock()
		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success: false,
			Message: "File does not exist",
		})
	}
	c.directoryMutex.RUnlock()

	// Create a file ID from the path
	fileID := types.FileID(path)

	// Split data into chunks (no locks held)
	chunks, err := c.chunkManager.SplitIntoChunks(data, fileID)
	if err != nil {
		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to split data into chunks: %v", err),
		})
	}

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
		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success: false,
			Message: "No healthy storage nodes available",
		})
	}

	// Allocate chunks to nodes (no locks held)
	distStrategy := storage.NewDistributionStrategy(2)
	allocations, err := distStrategy.AllocateChunks(chunks, nodeList)
	if err != nil {
		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to allocate chunks: %v", err),
		})
	}

	// Store chunks in parallel but maintain order
	storedChunks := make([]types.ChunkID, len(chunks))
	successFlags := make([]bool, len(chunks))
	errorChan := make(chan error, len(chunks)*2) // replication factor of 2

	var wg sync.WaitGroup
	for idx, chunk := range chunks {
		wg.Add(1)
		go func(index int, ch types.Chunk) {
			defer wg.Done()

			nodeIDs := allocations[ch.ID]
			successCount := 0

			for _, nodeID := range nodeIDs {
				c.nodeMutex.RLock()
				node := c.nodes[nodeID]
				c.nodeMutex.RUnlock()

				if node == nil {
					continue
				}

				// Store chunk on node
				if err := c.storeChunkOnNode(ctx, node, ch); err != nil {
					errorChan <- fmt.Errorf("failed to store chunk %s on node %s: %w", ch.ID, nodeID, err)
					continue
				}

				successCount++
				c.logger.Debug("Stored chunk on node",
					zap.String("node", string(nodeID)),
					zap.String("chunk", string(ch.ID)))
			}

			if successCount > 0 {
				storedChunks[index] = ch.ID
				successFlags[index] = true
			} else {
				errorChan <- fmt.Errorf("failed to store chunk %s on any node", ch.ID)
			}
		}(idx, chunk)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(errorChan)

	// Collect only successful chunks in order
	var finalStoredChunks []types.ChunkID
	for i, success := range successFlags {
		if success {
			finalStoredChunks = append(finalStoredChunks, storedChunks[i])
		}
	}

	// Check for errors
	var errs []error
	for err := range errorChan {
		errs = append(errs, err)
	}

	if len(finalStoredChunks) == 0 {
		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success: false,
			Message: fmt.Sprintf("Failed to store any chunks: %v", errs),
		})
	}

	// Update chunk allocations (merge, don't overwrite)
	c.chunkMutex.Lock()
	for chunkID, nodeIDs := range allocations {
		c.chunkAllocations[chunkID] = nodeIDs
	}
	c.fileChunks[path] = finalStoredChunks
	c.chunkMutex.Unlock()

	// Update file metadata (brief lock)
	c.directoryMutex.Lock()
	if fileEntry, exists := c.fileEntries[path]; exists {
		fileEntry.Size = int64(len(data))
		fileEntry.Modified = time.Now()
		fileEntry.ChunkIDs = finalStoredChunks
	}
	c.directoryMutex.Unlock()

	c.logger.Info("File written with parallel chunks",
		zap.String("path", path),
		zap.Int("chunks_created", len(chunks)),
		zap.Int("chunks_stored", len(finalStoredChunks)),
		zap.Int64("size", int64(len(data))))

	return stream.SendAndClose(&protocol.WriteFileStreamResponse{
		Success:       true,
		BytesWritten:  int64(len(data)),
		Message:       fmt.Sprintf("Data written successfully in %d chunks", len(finalStoredChunks)),
		ChunksCreated: int32(len(finalStoredChunks)),
	})
}

// ReadFileStreamStandard handles streaming file downloads for large files (original implementation)
func (c *Coordinator) ReadFileStreamStandard(req *protocol.ReadFileStreamRequest, stream protocol.Coordinator_ReadFileStreamServer) error {
	c.logger.Debug("ReadFileStream request", zap.String("path", req.Path))

	// Get file metadata
	c.directoryMutex.RLock()
	fileEntry, exists := c.fileEntries[req.Path]
	if !exists {
		c.directoryMutex.RUnlock()
		return fmt.Errorf("file does not exist: %s", req.Path)
	}
	chunkIDs := fileEntry.ChunkIDs
	fileSize := fileEntry.Size
	c.directoryMutex.RUnlock()

	// Send header
	header := &protocol.ReadFileStreamResponse{
		Data: &protocol.ReadFileStreamResponse_Header{
			Header: &protocol.ReadFileStreamHeader{
				// Header fields are minimal in current protocol
			},
		},
	}

	if err := stream.Send(header); err != nil {
		return err
	}

	// If file is empty, we're done
	if len(chunkIDs) == 0 {
		c.logger.Debug("File is empty, no chunks to send", zap.String("path", req.Path))
		return nil
	}

	// Get chunk allocations
	c.chunkMutex.RLock()
	allocations := c.chunkAllocations
	c.chunkMutex.RUnlock()

	// Stream chunks in order
	for i, chunkID := range chunkIDs {
		nodeIDs := allocations[chunkID]
		if len(nodeIDs) == 0 {
			return fmt.Errorf("no nodes found for chunk %s", chunkID)
		}

		// Try to retrieve from first available node
		var chunkData []byte
		var retrieved bool

		for _, nodeID := range nodeIDs {
			c.nodeMutex.RLock()
			node := c.nodes[nodeID]
			c.nodeMutex.RUnlock()

			if node == nil || !node.IsHealthy {
				continue
			}

			data, err := c.retrieveChunkFromNode(stream.Context(), node, chunkID)
			if err != nil {
				c.logger.Warn("Failed to retrieve chunk from node",
					zap.String("node", string(nodeID)),
					zap.String("chunk", string(chunkID)),
					zap.Error(err))
				continue
			}

			chunkData = data
			retrieved = true
			break
		}

		if !retrieved {
			return fmt.Errorf("failed to retrieve chunk %s from any node", chunkID)
		}

		// Send chunk data
		chunkResp := &protocol.ReadFileStreamResponse{
			Data: &protocol.ReadFileStreamResponse_ChunkData{
				ChunkData: chunkData,
			},
		}

		if err := stream.Send(chunkResp); err != nil {
			return err
		}

		c.logger.Debug("Streamed chunk",
			zap.String("path", req.Path),
			zap.Int("chunk_index", i),
			zap.Int("chunk_size", len(chunkData)))
	}

	c.logger.Info("File streamed successfully",
		zap.String("path", req.Path),
		zap.Int("chunks_sent", len(chunkIDs)),
		zap.Int64("total_size", fileSize))

	return nil
}

// retrieveChunkFromNode retrieves a single chunk from a node
func (c *Coordinator) retrieveChunkFromNode(ctx context.Context, node *types.StorageNode, chunkID types.ChunkID) ([]byte, error) {
	conn, err := c.getNodeConnection(node.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	client := protocol.NewNodeClient(conn)
	retrieveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := client.RetrieveChunk(retrieveCtx, &protocol.RetrieveChunkRequest{
		ChunkId: string(chunkID),
	})

	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf("node rejected chunk retrieval")
	}

	return resp.Data, nil
}

// getNodeConnection returns a pooled connection to a node
func (c *Coordinator) getNodeConnection(address string) (*grpc.ClientConn, error) {
	nodeConnMutex.RLock()
	conn, exists := nodeConnections[address]
	nodeConnMutex.RUnlock()

	if exists && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	// Create new connection
	nodeConnMutex.Lock()
	defer nodeConnMutex.Unlock()

	// Double-check after acquiring write lock
	conn, exists = nodeConnections[address]
	if exists && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	// Create new connection
	newConn, err := grpc.Dial(address, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	nodeConnections[address] = newConn
	return newConn, nil
}
