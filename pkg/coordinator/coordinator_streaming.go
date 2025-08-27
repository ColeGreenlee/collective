package coordinator

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"collective/pkg/client"
	"collective/pkg/protocol"
	"collective/pkg/storage"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

const (
	// StreamBufferSize is the size of data to accumulate before processing
	// Using 3MB chunks to stay safely under gRPC 4MB limit with metadata overhead
	StreamBufferSize = 3 * 1024 * 1024 // 3MB (leaves room for protocol overhead)

	// MaxConcurrentChunks limits parallel chunk operations to control memory
	MaxConcurrentChunks = 10
)

// WriteFileStream handles streaming file uploads with minimal memory usage
func (c *Coordinator) WriteFileStream(stream protocol.Coordinator_WriteFileStreamServer) error {
	var path string
	var fileID types.FileID
	var headerReceived bool
	var totalReceived int64
	var chunkIndex int

	// Channel for streaming chunks to processor
	chunkChan := make(chan types.Chunk, MaxConcurrentChunks)
	errorChan := make(chan error, 1)
	doneChan := make(chan []types.ChunkID, 1)

	// Start chunk processor goroutine
	go func() {
		chunks, err := c.processStreamingChunks(stream.Context(), chunkChan, path)
		if err != nil {
			errorChan <- err
			return
		}
		doneChan <- chunks
	}()

	// Buffer for accumulating streaming data
	buffer := make([]byte, 0, StreamBufferSize)

	// Process incoming stream
	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			close(chunkChan)
			return fmt.Errorf("stream receive error: %w", err)
		}

		switch data := req.Data.(type) {
		case *protocol.WriteFileStreamRequest_Header:
			if headerReceived {
				close(chunkChan)
				return fmt.Errorf("header already received")
			}

			path = data.Header.Path
			fileID = types.FileID(path)
			headerReceived = true

			c.logger.Info("Streaming upload started",
				zap.String("path", path),
				zap.String("method", "optimized"))

			// Verify file exists
			c.directoryMutex.RLock()
			_, exists := c.fileEntries[path]
			c.directoryMutex.RUnlock()

			if !exists {
				close(chunkChan)
				return stream.SendAndClose(&protocol.WriteFileStreamResponse{
					Success: false,
					Message: "File does not exist",
				})
			}

		case *protocol.WriteFileStreamRequest_ChunkData:
			if !headerReceived {
				close(chunkChan)
				return fmt.Errorf("header not received")
			}

			// Accumulate data in buffer
			buffer = append(buffer, data.ChunkData...)
			totalReceived += int64(len(data.ChunkData))

			// Process buffer when it reaches threshold
			for len(buffer) >= StreamBufferSize {
				// Extract a chunk worth of data
				chunkData := buffer[:StreamBufferSize]
				buffer = buffer[StreamBufferSize:]

				// Create chunk
				chunk := types.Chunk{
					ID:     c.chunkManager.GenerateChunkID(fileID, chunkIndex),
					FileID: fileID,
					Index:  chunkIndex,
					Data:   chunkData,
				}

				c.logger.Debug("Streaming chunk ready",
					zap.String("chunk_id", string(chunk.ID)),
					zap.Int("index", chunkIndex),
					zap.Int("size", len(chunkData)),
					zap.Int64("total_received", totalReceived))

				// Send chunk for processing (blocks if channel full)
				select {
				case chunkChan <- chunk:
					chunkIndex++
				case err := <-errorChan:
					close(chunkChan)
					return fmt.Errorf("chunk processing error: %w", err)
				case <-stream.Context().Done():
					close(chunkChan)
					return stream.Context().Err()
				}
			}
		}
	}

	// Process any remaining data in buffer
	if len(buffer) > 0 {
		chunk := types.Chunk{
			ID:     c.chunkManager.GenerateChunkID(fileID, chunkIndex),
			FileID: fileID,
			Index:  chunkIndex,
			Data:   buffer,
		}

		c.logger.Debug("Final chunk ready",
			zap.String("chunk_id", string(chunk.ID)),
			zap.Int("index", chunkIndex),
			zap.Int("size", len(buffer)))

		select {
		case chunkChan <- chunk:
		case err := <-errorChan:
			close(chunkChan)
			return fmt.Errorf("chunk processing error: %w", err)
		}
	}

	// Signal end of chunks
	close(chunkChan)

	// Wait for processing to complete
	select {
	case storedChunks := <-doneChan:
		// Update file metadata
		c.updateFileMetadata(path, storedChunks, totalReceived)

		c.logger.Info("Streaming upload completed",
			zap.String("path", path),
			zap.Int64("total_bytes", totalReceived),
			zap.Int("chunks_stored", len(storedChunks)))

		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success:       true,
			BytesWritten:  totalReceived,
			Message:       fmt.Sprintf("Stored %d chunks using streaming", len(storedChunks)),
			ChunksCreated: int32(len(storedChunks)),
		})

	case err := <-errorChan:
		return stream.SendAndClose(&protocol.WriteFileStreamResponse{
			Success: false,
			Message: fmt.Sprintf("Processing error: %v", err),
		})

	case <-stream.Context().Done():
		return stream.Context().Err()
	}
}

// processStreamingChunks processes chunks as they arrive from the stream
func (c *Coordinator) processStreamingChunks(ctx context.Context, chunkChan <-chan types.Chunk, path string) ([]types.ChunkID, error) {
	// Get file lock
	fileLock := c.getFileLock(path)
	fileLock.Lock()
	defer fileLock.Unlock()

	// Get healthy nodes
	c.nodeMutex.RLock()
	var nodeList []*types.StorageNode
	for _, node := range c.nodes {
		if node.IsHealthy {
			nodeList = append(nodeList, node)
		}
	}
	c.nodeMutex.RUnlock()

	if len(nodeList) == 0 {
		return nil, fmt.Errorf("no healthy storage nodes available")
	}

	// Initialize distribution strategy
	distStrategy := storage.NewDistributionStrategy(2)

	// Process chunks with controlled concurrency
	var storedChunks []types.ChunkID
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, MaxConcurrentChunks)

	for chunk := range chunkChan {
		// Allocate nodes for this chunk
		allocations, err := distStrategy.AllocateChunks([]types.Chunk{chunk}, nodeList)
		if err != nil {
			return nil, fmt.Errorf("failed to allocate chunk: %w", err)
		}

		// Acquire semaphore slot
		semaphore <- struct{}{}
		wg.Add(1)

		// Store chunk asynchronously
		go func(ch types.Chunk, nodeIDs []types.NodeID) {
			defer wg.Done()
			defer func() { <-semaphore }() // Release slot

			successCount := 0
			for _, nodeID := range nodeIDs {
				c.nodeMutex.RLock()
				node := c.nodes[nodeID]
				c.nodeMutex.RUnlock()

				if node == nil {
					continue
				}

				if err := c.storeChunkOnNode(ctx, node, ch); err != nil {
					c.logger.Warn("Failed to store chunk on node",
						zap.String("node", string(nodeID)),
						zap.String("chunk", string(ch.ID)),
						zap.Error(err))
					continue
				}

				successCount++
			}

			if successCount > 0 {
				// Update allocations
				c.chunkMutex.Lock()
				c.chunkAllocations[ch.ID] = nodeIDs
				c.chunkMutex.Unlock()

				c.logger.Debug("Chunk stored",
					zap.String("chunk", string(ch.ID)),
					zap.Int("replicas", successCount))
			}
		}(chunk, allocations[chunk.ID])

		// Track stored chunk
		storedChunks = append(storedChunks, chunk.ID)
	}

	// Wait for all chunks to be stored
	wg.Wait()

	// Update chunk mappings
	c.chunkMutex.Lock()
	c.fileChunks[path] = storedChunks
	c.chunkMutex.Unlock()

	return storedChunks, nil
}

// updateFileMetadata updates file entry after streaming upload
func (c *Coordinator) updateFileMetadata(path string, chunkIDs []types.ChunkID, size int64) {
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()

	if entry, exists := c.fileEntries[path]; exists {
		entry.Size = size
		entry.Modified = time.Now()
		entry.ChunkIDs = chunkIDs

		c.logger.Debug("Updated file metadata",
			zap.String("path", path),
			zap.Int64("size", size),
			zap.Int("chunks", len(chunkIDs)))
	}
}

// ReadFileStream handles streaming file downloads with minimal memory usage
func (c *Coordinator) ReadFileStream(req *protocol.ReadFileStreamRequest, stream protocol.Coordinator_ReadFileStreamServer) error {
	c.logger.Debug("Optimized streaming read",
		zap.String("path", req.Path))

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
	if err := stream.Send(&protocol.ReadFileStreamResponse{
		Data: &protocol.ReadFileStreamResponse_Header{
			Header: &protocol.ReadFileStreamHeader{},
		},
	}); err != nil {
		return fmt.Errorf("failed to send header: %w", err)
	}

	if len(chunkIDs) == 0 {
		return nil // Empty file
	}

	// Stream chunks with controlled concurrency
	c.chunkMutex.RLock()
	allocations := c.chunkAllocations
	c.chunkMutex.RUnlock()

	totalSent := int64(0)

	for i, chunkID := range chunkIDs {
		nodeIDs := allocations[chunkID]
		if len(nodeIDs) == 0 {
			return fmt.Errorf("no nodes for chunk %s", chunkID)
		}

		// Retrieve chunk from first available node
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
				c.logger.Warn("Failed to retrieve chunk",
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
			return fmt.Errorf("failed to retrieve chunk %s", chunkID)
		}

		// Send chunk
		if err := stream.Send(&protocol.ReadFileStreamResponse{
			Data: &protocol.ReadFileStreamResponse_ChunkData{
				ChunkData: chunkData,
			},
		}); err != nil {
			return fmt.Errorf("failed to send chunk %d: %w", i, err)
		}

		totalSent += int64(len(chunkData))

		c.logger.Debug("Streamed chunk",
			zap.String("path", req.Path),
			zap.Int("chunk_index", i),
			zap.Int("chunk_size", len(chunkData)),
			zap.Int64("total_sent", totalSent),
			zap.Int64("file_size", fileSize))
	}

	c.logger.Info("File streamed",
		zap.String("path", req.Path),
		zap.Int("chunks", len(chunkIDs)),
		zap.Int64("bytes", totalSent))

	return nil
}

// Connection pool for node connections
var (
	nodeConnections = make(map[string]*grpc.ClientConn)
	nodeConnMutex   sync.RWMutex
)

// retrieveChunkFromNode retrieves a chunk from a specific storage node
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

	// Create new secure connection
	// Note: This uses the coordinator's auth config if available
	var newConn *grpc.ClientConn
	var err error
	if c.authConfig != nil {
		newConn, err = client.CreateAuthenticatedConnection(context.Background(), address, c.authConfig)
	} else {
		// Fallback for nodes without auth configured
		newConn, err = grpc.Dial(address, grpc.WithInsecure())
	}
	if err != nil {
		return nil, fmt.Errorf("failed to dial node: %w", err)
	}

	// Store in pool
	nodeConnections[address] = newConn
	return newConn, nil
}
