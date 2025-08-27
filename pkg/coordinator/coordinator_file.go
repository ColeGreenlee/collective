package coordinator

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"collective/pkg/protocol"
	"collective/pkg/storage"
	"collective/pkg/types"

	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// CreateFile creates a new file entry
func (c *Coordinator) CreateFile(ctx context.Context, req *protocol.CreateFileRequest) (*protocol.CreateFileResponse, error) {
	c.logger.Debug("CreateFile request", zap.String("path", req.Path))
	
	// Get file lock for this specific file
	fileLock := c.getFileLock(req.Path)
	fileLock.Lock()
	defer fileLock.Unlock()
	
	c.directoryMutex.Lock()
	defer c.directoryMutex.Unlock()
	
	// Check if file already exists
	if _, exists := c.fileEntries[req.Path]; exists {
		return &protocol.CreateFileResponse{
			Success: false,
			Message: "File already exists",
		}, nil
	}
	
	// Check if parent directory exists
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
	mode := os.FileMode(0644)
	if req.Mode != 0 {
		mode = os.FileMode(req.Mode)
	}
	
	fileEntry := &types.FileEntry{
		Path:     req.Path,
		Size:     0,
		Mode:     mode,
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
	
	c.logger.Info("File created", zap.String("path", req.Path))
	
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
	if !exists {
		c.directoryMutex.RUnlock()
		return &protocol.ReadFileResponse{
			Success: false,
			Data:    nil,
		}, nil
	}
	chunkIDs := fileEntry.ChunkIDs
	c.directoryMutex.RUnlock()
	
	if len(chunkIDs) == 0 {
		// Empty file
		return &protocol.ReadFileResponse{
			Success:   true,
			Data:      []byte{},
			BytesRead: 0,
		}, nil
	}
	
	// Retrieve chunks from storage nodes
	chunks, err := c.retrieveChunksFromNodes(ctx, chunkIDs)
	if err != nil {
		c.logger.Error("Failed to retrieve chunks",
			zap.String("path", req.Path),
			zap.Error(err))
		return &protocol.ReadFileResponse{
			Success: false,
			Data:    nil,
		}, nil
	}
	
	// Reassemble chunks into file data
	resultData, _ := c.chunkManager.ReassembleChunks(chunks)
	
	// Handle offset and length
	if req.Offset > 0 {
		if req.Offset >= int64(len(resultData)) {
			// Offset beyond file size
			return &protocol.ReadFileResponse{
				Success:   true,
				Data:      []byte{},
				BytesRead: 0,
			}, nil
		}
		resultData = resultData[req.Offset:]
	}
	
	if req.Length > 0 && req.Length < int64(len(resultData)) {
		resultData = resultData[:req.Length]
	}
	
	c.logger.Info("File read successfully",
		zap.String("path", req.Path),
		zap.Int("chunks_retrieved", len(chunks)),
		zap.Int64("bytes_read", int64(len(resultData))))
	
	return &protocol.ReadFileResponse{
		Success:   true,
		Data:      resultData,
		BytesRead: int64(len(resultData)),
	}, nil
}

// WriteFile writes data to a file
func (c *Coordinator) WriteFile(ctx context.Context, req *protocol.WriteFileRequest) (*protocol.WriteFileResponse, error) {
	c.logger.Debug("WriteFile request",
		zap.String("path", req.Path),
		zap.Int64("offset", req.Offset),
		zap.Int("data_size", len(req.Data)))
	
	// Get file lock for this specific file
	fileLock := c.getFileLock(req.Path)
	fileLock.Lock()
	defer fileLock.Unlock()
	
	// Quick check if file exists (minimal lock time)
	c.directoryMutex.RLock()
	_, exists := c.fileEntries[req.Path]
	if !exists {
		c.directoryMutex.RUnlock()
		return &protocol.WriteFileResponse{
			Success:      false,
			BytesWritten: 0,
			Message:      "File does not exist",
		}, nil
	}
	c.directoryMutex.RUnlock()
	
	// For simplicity, we'll handle full file writes (offset 0) for now
	// TODO: Support partial writes and appends
	if req.Offset != 0 {
		// For now, we'll handle append-style writes
		c.logger.Warn("Non-zero offset write, treating as overwrite", zap.Int64("offset", req.Offset))
	}
	
	// Create a file ID from the path
	fileID := types.FileID(req.Path)
	
	// Split data into chunks (no locks held)
	chunks, err := c.chunkManager.SplitIntoChunks(req.Data, fileID)
	if err != nil {
		return &protocol.WriteFileResponse{
			Success:      false,
			BytesWritten: 0,
			Message:      fmt.Sprintf("Failed to split data into chunks: %v", err),
		}, nil
	}
	
	// Get healthy nodes for chunk storage (no locks held during chunk operations)
	c.nodeMutex.RLock()
	nodeList := []*types.StorageNode{}
	for _, node := range c.nodes {
		if node.IsHealthy {
			nodeList = append(nodeList, node)
		}
	}
	c.nodeMutex.RUnlock()
	
	if len(nodeList) == 0 {
		return &protocol.WriteFileResponse{
			Success:      false,
			BytesWritten: 0,
			Message:      "No healthy storage nodes available",
		}, nil
	}
	
	// Allocate chunks to nodes (replication factor = 2)
	distStrategy := storage.NewDistributionStrategy(2)
	allocations, err := distStrategy.AllocateChunks(chunks, nodeList)
	if err != nil {
		return &protocol.WriteFileResponse{
			Success:      false,
			BytesWritten: 0,
			Message:      fmt.Sprintf("Failed to allocate chunks: %v", err),
		}, nil
	}
	
	// Store chunks on allocated nodes
	storedChunks := []types.ChunkID{}
	for _, chunk := range chunks {
		nodeIDs := allocations[chunk.ID]
		successCount := 0
		
		for _, nodeID := range nodeIDs {
			// Get node info
			c.nodeMutex.RLock()
			node := c.nodes[nodeID]
			c.nodeMutex.RUnlock()
			
			if node == nil {
				continue
			}
			
			// Connect to node and store chunk
			conn, err := grpc.Dial(node.Address, grpc.WithInsecure())
			if err != nil {
				c.logger.Warn("Failed to connect to node", zap.String("node", string(nodeID)), zap.Error(err))
				continue
			}
			
			client := protocol.NewNodeClient(conn)
			storeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			
			resp, err := client.StoreChunk(storeCtx, &protocol.StoreChunkRequest{
				ChunkId: string(chunk.ID),
				FileId:  string(chunk.FileID),
				Index:   int32(chunk.Index),
				Data:    chunk.Data,
			})
			
			cancel()
			conn.Close()
			
			if err != nil || !resp.Success {
				c.logger.Warn("Failed to store chunk on node", 
					zap.String("node", string(nodeID)), 
					zap.String("chunk", string(chunk.ID)),
					zap.Error(err))
				continue
			}
			
			successCount++
			c.logger.Debug("Stored chunk on node", 
				zap.String("node", string(nodeID)),
				zap.String("chunk", string(chunk.ID)),
				zap.Int("index", chunk.Index))
		}
		
		if successCount > 0 {
			storedChunks = append(storedChunks, chunk.ID)
		} else {
			c.logger.Error("Failed to store chunk on any node", zap.String("chunk", string(chunk.ID)))
		}
	}
	
	// Update chunk allocations (merge, don't overwrite)
	c.chunkMutex.Lock()
	for chunkID, nodeIDs := range allocations {
		c.chunkAllocations[chunkID] = nodeIDs
	}
	c.fileChunks[req.Path] = storedChunks
	c.chunkMutex.Unlock()
	
	// Update file entry metadata (brief lock)
	c.directoryMutex.Lock()
	if fileEntry, exists := c.fileEntries[req.Path]; exists {
		fileEntry.Size = int64(len(req.Data))
		fileEntry.Modified = time.Now()
		fileEntry.ChunkIDs = storedChunks
	}
	c.directoryMutex.Unlock()
	
	c.logger.Info("File written with chunks",
		zap.String("path", req.Path),
		zap.Int("chunks_created", len(chunks)),
		zap.Int("chunks_stored", len(storedChunks)),
		zap.Int64("size", int64(len(req.Data))))
	
	return &protocol.WriteFileResponse{
		Success:      true,
		BytesWritten: int64(len(req.Data)),
		Message:      fmt.Sprintf("Data written successfully in %d chunks", len(storedChunks)),
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
	
	// Delete associated chunks from nodes
	c.chunkMutex.Lock()
	allocations := c.chunkAllocations
	delete(c.fileChunks, req.Path)
	c.chunkMutex.Unlock()
	
	deletedChunks := 0
	for _, chunkID := range fileEntry.ChunkIDs {
		nodeIDs := allocations[chunkID]
		for _, nodeID := range nodeIDs {
			c.nodeMutex.RLock()
			node := c.nodes[nodeID]
			c.nodeMutex.RUnlock()
			
			if node == nil {
				continue
			}
			
			// Connect to node and delete chunk
			conn, err := grpc.Dial(node.Address, grpc.WithInsecure())
			if err != nil {
				c.logger.Warn("Failed to connect to node for chunk deletion", 
					zap.String("node", string(nodeID)), 
					zap.Error(err))
				continue
			}
			
			client := protocol.NewNodeClient(conn)
			deleteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			
			resp, err := client.DeleteChunk(deleteCtx, &protocol.DeleteChunkRequest{
				ChunkId: string(chunkID),
			})
			
			cancel()
			conn.Close()
			
			if err != nil || !resp.Success {
				c.logger.Warn("Failed to delete chunk from node",
					zap.String("node", string(nodeID)),
					zap.String("chunk", string(chunkID)),
					zap.Error(err))
			} else {
				deletedChunks++
			}
		}
		
		// Remove from allocations
		c.chunkMutex.Lock()
		delete(c.chunkAllocations, chunkID)
		c.chunkMutex.Unlock()
	}
	
	c.logger.Info("File deleted",
		zap.String("path", req.Path),
		zap.Int("chunks_deleted", deletedChunks))
	
	return &protocol.DeleteFileResponse{
		Success: true,
		Message: "File deleted successfully",
	}, nil
}

// getFileLock returns the lock for a specific file, creating it if needed
func (c *Coordinator) getFileLock(path string) *sync.RWMutex {
	c.fileLockMutex.Lock()
	defer c.fileLockMutex.Unlock()
	
	lock, exists := c.fileLocks[path]
	if !exists {
		lock = &sync.RWMutex{}
		c.fileLocks[path] = lock
	}
	return lock
}