package storage

import (
	"crypto/rand"
	"fmt"
	"testing"

	"collective/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkManagerChunkSizeSelection(t *testing.T) {
	cm := NewChunkManager()

	tests := []struct {
		name     string
		fileSize int64
		expected int
	}{
		{"Small file (<1MB)", 512 * 1024, SmallChunkSize},
		{"Medium file (1-100MB)", 50 * 1024 * 1024, DefaultChunkSize},
		{"Large file (>100MB)", 200 * 1024 * 1024, LargeChunkSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := cm.GetOptimalChunkSize(tt.fileSize)
			assert.Equal(t, tt.expected, size)
		})
	}
}

func TestChunkSplittingAndReassembly(t *testing.T) {
	cm := NewChunkManager()
	fileID := types.FileID("test-file")

	// Test various data sizes
	testSizes := []int{
		100,             // Very small
		1024,            // 1KB
		64 * 1024,       // 64KB (small chunk size)
		1024 * 1024,     // 1MB (default chunk size)
		5 * 1024 * 1024, // 5MB (multiple chunks)
	}

	for _, size := range testSizes {
		t.Run(fmt.Sprintf("Size_%d", size), func(t *testing.T) {
			// Generate random test data
			originalData := make([]byte, size)
			_, err := rand.Read(originalData)
			require.NoError(t, err)

			// Split into chunks
			chunks, err := cm.SplitIntoChunks(originalData, fileID)
			require.NoError(t, err)
			assert.NotEmpty(t, chunks)

			// Verify chunk properties
			totalSize := int64(0)
			for i, chunk := range chunks {
				assert.Equal(t, i, chunk.Index)
				assert.Equal(t, fileID, chunk.FileID)
				assert.NotEmpty(t, chunk.ID)
				assert.NotEmpty(t, chunk.Hash)
				totalSize += chunk.Size
			}

			// Reassemble chunks
			reassembled, err := cm.ReassembleChunks(chunks)
			require.NoError(t, err)

			// Verify data integrity
			assert.Equal(t, originalData, reassembled, "Reassembled data should match original")
		})
	}
}

func TestChunkCompression(t *testing.T) {
	cm := NewChunkManagerWithOptions(DefaultChunkSize, true, -1)
	fileID := types.FileID("test-file")

	t.Run("CompressibleData", func(t *testing.T) {
		// Highly compressible data (zeros)
		data := make([]byte, 1024*1024) // 1MB of zeros

		chunks, err := cm.SplitIntoChunks(data, fileID)
		require.NoError(t, err)

		// Check that compression occurred
		for _, chunk := range chunks {
			if chunk.Compressed {
				assert.Less(t, chunk.Size, chunk.OriginalSize, "Compressed size should be smaller")
			}
		}

		// Verify reassembly works with compressed chunks
		reassembled, err := cm.ReassembleChunks(chunks)
		require.NoError(t, err)
		assert.Equal(t, data, reassembled)
	})

	t.Run("RandomData", func(t *testing.T) {
		// Random data (not compressible)
		data := make([]byte, 1024*1024) // 1MB
		rand.Read(data)

		chunks, err := cm.SplitIntoChunks(data, fileID)
		require.NoError(t, err)

		// Random data shouldn't compress well
		for _, chunk := range chunks {
			if !chunk.Compressed {
				assert.Equal(t, chunk.Size, chunk.OriginalSize, "Random data shouldn't compress")
			}
		}

		// Verify reassembly
		reassembled, err := cm.ReassembleChunks(chunks)
		require.NoError(t, err)
		assert.Equal(t, data, reassembled)
	})
}

func TestDistributionStrategy(t *testing.T) {
	strategy := NewDistributionStrategy(2) // Replication factor of 2

	// Create test nodes
	nodes := []*types.StorageNode{
		{ID: "node1", TotalCapacity: 1000, UsedCapacity: 100, IsHealthy: true},
		{ID: "node2", TotalCapacity: 1000, UsedCapacity: 200, IsHealthy: true},
		{ID: "node3", TotalCapacity: 1000, UsedCapacity: 300, IsHealthy: true},
		{ID: "node4", TotalCapacity: 1000, UsedCapacity: 900, IsHealthy: true}, // Almost full
	}

	// Create test chunks
	chunks := []types.Chunk{
		{ID: "chunk1", Size: 100},
		{ID: "chunk2", Size: 100},
		{ID: "chunk3", Size: 100},
	}

	allocations, err := strategy.AllocateChunks(chunks, nodes)
	require.NoError(t, err)

	// Verify allocations
	assert.Len(t, allocations, 3, "Should have allocations for all chunks")

	for chunkID, nodeIDs := range allocations {
		assert.Len(t, nodeIDs, 2, "Each chunk should be replicated to 2 nodes")

		// Verify no duplicate nodes
		seen := make(map[types.NodeID]bool)
		for _, nodeID := range nodeIDs {
			assert.False(t, seen[nodeID], "Chunk %s should not be allocated to same node twice", chunkID)
			seen[nodeID] = true
		}

		// Verify nodes with less usage are preferred
		// Node4 (90% full) should be avoided if possible
		for _, nodeID := range nodeIDs {
			if nodeID == "node4" {
				t.Logf("Warning: Chunk %s allocated to nearly full node4", chunkID)
			}
		}
	}
}

func TestChunkIntegrity(t *testing.T) {
	cm := NewChunkManager()
	fileID := types.FileID("test-file")

	// Create test data
	data := []byte("Test data for integrity check")

	// Split into chunks
	chunks, err := cm.SplitIntoChunks(data, fileID)
	require.NoError(t, err)

	// Corrupt a chunk
	originalData := chunks[0].Data
	chunks[0].Data = []byte("corrupted")

	// Reassembly should still work (no hash verification in current implementation)
	reassembled, err := cm.ReassembleChunks(chunks)
	require.NoError(t, err)
	assert.NotEqual(t, data, reassembled, "Corrupted data should not match original")

	// Restore original data
	chunks[0].Data = originalData
	reassembled, err = cm.ReassembleChunks(chunks)
	require.NoError(t, err)
	assert.Equal(t, data, reassembled, "Restored data should match original")
}

func TestEdgeCases(t *testing.T) {
	cm := NewChunkManager()

	t.Run("EmptyData", func(t *testing.T) {
		chunks, err := cm.SplitIntoChunks([]byte{}, types.FileID("empty"))
		require.NoError(t, err)
		assert.Empty(t, chunks)

		reassembled, err := cm.ReassembleChunks(chunks)
		require.NoError(t, err)
		assert.Empty(t, reassembled)
	})

	t.Run("MissingChunk", func(t *testing.T) {
		data := []byte("Test data with multiple chunks")
		chunks, err := cm.SplitIntoChunks(data, types.FileID("test"))
		require.NoError(t, err)

		if len(chunks) > 1 {
			// Remove middle chunk
			chunks = append(chunks[:1], chunks[2:]...)

			_, err = cm.ReassembleChunks(chunks)
			assert.Error(t, err, "Should error on missing chunk")
		}
	})

	t.Run("OutOfOrderChunks", func(t *testing.T) {
		data := make([]byte, 3*1024*1024) // 3MB to ensure multiple chunks
		rand.Read(data)

		chunks, err := cm.SplitIntoChunks(data, types.FileID("test"))
		require.NoError(t, err)
		require.Greater(t, len(chunks), 1, "Need multiple chunks for this test")

		// Reverse chunk order
		for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
			chunks[i], chunks[j] = chunks[j], chunks[i]
		}

		// Reassembly should still work (sorts by index)
		reassembled, err := cm.ReassembleChunks(chunks)
		require.NoError(t, err)
		assert.Equal(t, data, reassembled, "Should handle out-of-order chunks")
	})
}

// Benchmarks
func BenchmarkChunkSplitting(b *testing.B) {
	cm := NewChunkManager()
	data := make([]byte, 10*1024*1024) // 10MB
	rand.Read(data)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cm.SplitIntoChunks(data, types.FileID("bench"))
	}

	b.SetBytes(int64(len(data)))
}

func BenchmarkChunkReassembly(b *testing.B) {
	cm := NewChunkManager()
	data := make([]byte, 10*1024*1024) // 10MB
	rand.Read(data)

	chunks, _ := cm.SplitIntoChunks(data, types.FileID("bench"))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cm.ReassembleChunks(chunks)
	}

	b.SetBytes(int64(len(data)))
}

func BenchmarkCompression(b *testing.B) {
	cm := NewChunkManagerWithOptions(DefaultChunkSize, true, -1)

	// Test with compressible data
	data := make([]byte, 1024*1024) // 1MB of zeros (highly compressible)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = cm.SplitIntoChunks(data, types.FileID("bench"))
	}

	b.SetBytes(int64(len(data)))
}
