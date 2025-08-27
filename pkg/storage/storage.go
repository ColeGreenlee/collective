package storage

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"collective/pkg/types"
)

const (
	DefaultChunkSize = 1024 * 1024 // 1MB chunks
	SmallChunkSize   = 64 * 1024   // 64KB for small files
	LargeChunkSize   = 4 * 1024 * 1024 // 4MB for large files
	
	SmallFileThreshold = 1024 * 1024     // Files < 1MB
	LargeFileThreshold = 100 * 1024 * 1024 // Files > 100MB
)

type ChunkManager struct {
	chunkSize         int
	enableCompression bool
	compressionLevel  int
}

func NewChunkManager() *ChunkManager {
	return &ChunkManager{
		chunkSize:         DefaultChunkSize,
		enableCompression: false, // Disabled for performance testing
		compressionLevel:  gzip.DefaultCompression,
	}
}

// NewChunkManagerWithOptions creates a ChunkManager with custom options
func NewChunkManagerWithOptions(chunkSize int, enableCompression bool, compressionLevel int) *ChunkManager {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if compressionLevel < gzip.HuffmanOnly || compressionLevel > gzip.BestCompression {
		compressionLevel = gzip.DefaultCompression
	}
	return &ChunkManager{
		chunkSize:         chunkSize,
		enableCompression: enableCompression,
		compressionLevel:  compressionLevel,
	}
}

// GetOptimalChunkSize determines the best chunk size for a given file size
func (cm *ChunkManager) GetOptimalChunkSize(fileSize int64) int {
	if fileSize < SmallFileThreshold {
		return SmallChunkSize
	} else if fileSize > LargeFileThreshold {
		return LargeChunkSize
	}
	return DefaultChunkSize
}

// SplitIntoChunks divides data into optimally-sized chunks
func (cm *ChunkManager) SplitIntoChunks(data []byte, fileID types.FileID) ([]types.Chunk, error) {
	fileSize := int64(len(data))
	optimalChunkSize := cm.GetOptimalChunkSize(fileSize)
	
	// Override with optimal size if not manually set
	if cm.chunkSize == DefaultChunkSize {
		cm.chunkSize = optimalChunkSize
	}
	
	reader := bytes.NewReader(data)
	chunks := []types.Chunk{}
	index := 0

	buffer := make([]byte, cm.chunkSize)
	
	for {
		n, err := reader.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read data: %w", err)
		}
		
		chunkData := make([]byte, n)
		copy(chunkData, buffer[:n])
		
		// Compress chunk data if enabled
		originalSize := int64(n)
		if cm.enableCompression {
			compressed, err := cm.compressData(chunkData)
			if err == nil && len(compressed) < len(chunkData) {
				// Only use compressed version if it's actually smaller
				chunkData = compressed
			}
		}
		
		hash := sha256.Sum256(chunkData)
		// Replace slashes in fileID to avoid path issues
		safeFileID := strings.ReplaceAll(string(fileID), "/", "_")
		chunkID := types.ChunkID(fmt.Sprintf("%s-%d-%x", safeFileID, index, hash[:8]))
		
		chunk := types.Chunk{
			ID:           chunkID,
			FileID:       fileID,
			Index:        index,
			Size:         int64(len(chunkData)), // Size of actual data (compressed or not)
			OriginalSize: originalSize,           // Original uncompressed size
			Hash:         fmt.Sprintf("%x", hash),
			Data:         chunkData,
			Compressed:   len(chunkData) < int(originalSize), // Track if compressed
		}
		
		chunks = append(chunks, chunk)
		index++
	}
	
	return chunks, nil
}

// GenerateChunkID creates a unique chunk ID for a given file and index
func (cm *ChunkManager) GenerateChunkID(fileID types.FileID, index int) types.ChunkID {
	// Replace slashes in fileID to avoid path issues
	safeFileID := strings.ReplaceAll(string(fileID), "/", "_")
	// Use a simplified ID format for streaming chunks
	return types.ChunkID(fmt.Sprintf("%s-chunk-%d", safeFileID, index))
}

// ReassembleChunks combines chunks back into original data
func (cm *ChunkManager) ReassembleChunks(chunks []types.Chunk) ([]byte, error) {
	// Sort chunks by index
	sortedChunks := make([]types.Chunk, len(chunks))
	for _, chunk := range chunks {
		if chunk.Index >= len(chunks) {
			return nil, fmt.Errorf("invalid chunk index %d", chunk.Index)
		}
		sortedChunks[chunk.Index] = chunk
	}
	
	// Verify all chunks are present
	for i, chunk := range sortedChunks {
		if chunk.ID == "" {
			return nil, fmt.Errorf("missing chunk at index %d", i)
		}
	}
	
	// Concatenate chunk data, decompressing as needed
	var result bytes.Buffer
	for _, chunk := range sortedChunks {
		data := chunk.Data
		// Decompress if chunk was compressed
		if chunk.Compressed && cm.enableCompression {
			decompressed, err := cm.decompressData(chunk.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to decompress chunk %s: %w", chunk.ID, err)
			}
			data = decompressed
		}
		if _, err := result.Write(data); err != nil {
			return nil, fmt.Errorf("failed to write chunk data: %w", err)
		}
	}
	
	return result.Bytes(), nil
}

// VerifyChunk validates chunk integrity using its hash
func (cm *ChunkManager) VerifyChunk(chunk *types.Chunk) bool {
	hash := sha256.Sum256(chunk.Data)
	expectedHash := fmt.Sprintf("%x", hash)
	return chunk.Hash == expectedHash
}

// DistributionStrategy determines which nodes should store which chunks
type DistributionStrategy struct {
	replicationFactor int
}

func NewDistributionStrategy(replicationFactor int) *DistributionStrategy {
	if replicationFactor < 1 {
		replicationFactor = 2 // Default to 2x replication
	}
	return &DistributionStrategy{
		replicationFactor: replicationFactor,
	}
}

// AllocateChunks determines node assignments for chunks
func (ds *DistributionStrategy) AllocateChunks(chunks []types.Chunk, nodes []*types.StorageNode) (map[types.ChunkID][]types.NodeID, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes available for allocation")
	}
	
	allocations := make(map[types.ChunkID][]types.NodeID)
	
	// Filter healthy nodes
	healthyNodes := []*types.StorageNode{}
	for _, node := range nodes {
		if node.IsHealthy && node.TotalCapacity-node.UsedCapacity > 0 {
			healthyNodes = append(healthyNodes, node)
		}
	}
	
	if len(healthyNodes) == 0 {
		return nil, fmt.Errorf("no healthy nodes with available capacity")
	}
	
	// Simple round-robin distribution with replication
	for _, chunk := range chunks {
		nodeCount := min(ds.replicationFactor, len(healthyNodes))
		selectedNodes := []types.NodeID{}
		
		// Select nodes for this chunk
		for i := 0; i < nodeCount; i++ {
			// Start at different offset for each chunk to spread load
			nodeIndex := (int(chunk.Index) + i) % len(healthyNodes)
			node := healthyNodes[nodeIndex]
			
			// Check if node has capacity
			if node.TotalCapacity-node.UsedCapacity >= chunk.Size {
				selectedNodes = append(selectedNodes, node.ID)
			}
		}
		
		if len(selectedNodes) == 0 {
			return nil, fmt.Errorf("no nodes with sufficient capacity for chunk %s", chunk.ID)
		}
		
		allocations[chunk.ID] = selectedNodes
	}
	
	return allocations, nil
}

// EnsureMemberDiversity ensures chunks are distributed across different members
func (ds *DistributionStrategy) EnsureMemberDiversity(allocations map[types.ChunkID][]types.NodeID, nodes []*types.StorageNode) map[types.ChunkID][]types.NodeID {
	nodeToMember := make(map[types.NodeID]types.MemberID)
	for _, node := range nodes {
		nodeToMember[node.ID] = node.MemberID
	}
	
	// For each chunk, ensure replicas are on different members if possible
	for chunkID, nodeIDs := range allocations {
		memberSet := make(map[types.MemberID]bool)
		newNodeIDs := []types.NodeID{}
		
		// First pass: select one node per member
		for _, nodeID := range nodeIDs {
			memberID := nodeToMember[nodeID]
			if !memberSet[memberID] {
				memberSet[memberID] = true
				newNodeIDs = append(newNodeIDs, nodeID)
			}
		}
		
		// If we need more nodes, add duplicates from same members
		if len(newNodeIDs) < len(nodeIDs) {
			for _, nodeID := range nodeIDs {
				if len(newNodeIDs) >= len(nodeIDs) {
					break
				}
				// Add if not already included
				found := false
				for _, existing := range newNodeIDs {
					if existing == nodeID {
						found = true
						break
					}
				}
				if !found {
					newNodeIDs = append(newNodeIDs, nodeID)
				}
			}
		}
		
		allocations[chunkID] = newNodeIDs
	}
	
	return allocations
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// compressData compresses data using gzip
func (cm *ChunkManager) compressData(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buf, cm.compressionLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip writer: %w", err)
	}
	
	if _, err := writer.Write(data); err != nil {
		writer.Close()
		return nil, fmt.Errorf("failed to write compressed data: %w", err)
	}
	
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}
	
	return buf.Bytes(), nil
}

// decompressData decompresses gzip-compressed data
func (cm *ChunkManager) decompressData(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer reader.Close()
	
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read decompressed data: %w", err)
	}
	
	return decompressed, nil
}