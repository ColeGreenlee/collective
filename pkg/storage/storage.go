package storage

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"

	"collective/pkg/types"
)

const (
	DefaultChunkSize = 1024 * 1024 // 1MB chunks
)

type ChunkManager struct {
	chunkSize int
}

func NewChunkManager() *ChunkManager {
	return &ChunkManager{
		chunkSize: DefaultChunkSize,
	}
}

// SplitIntoChunks divides data into fixed-size chunks
func (cm *ChunkManager) SplitIntoChunks(data []byte, fileID types.FileID) ([]types.Chunk, error) {
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
		
		hash := sha256.Sum256(chunkData)
		chunkID := types.ChunkID(fmt.Sprintf("%s-%d-%x", fileID, index, hash[:8]))
		
		chunk := types.Chunk{
			ID:     chunkID,
			FileID: fileID,
			Index:  index,
			Size:   int64(n),
			Hash:   fmt.Sprintf("%x", hash),
			Data:   chunkData,
		}
		
		chunks = append(chunks, chunk)
		index++
	}
	
	return chunks, nil
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
	
	// Concatenate chunk data
	var result bytes.Buffer
	for _, chunk := range sortedChunks {
		if _, err := result.Write(chunk.Data); err != nil {
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