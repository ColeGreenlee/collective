package federation

import (
	"collective/pkg/types"
	"fmt"
	"time"
)

// PlacementController provides high-level chunk placement operations
type PlacementController struct {
	engine            *PlacementEngine
	failureHandler    *FailureHandler
	rebalancer        *Rebalancer
}

// FailureHandler handles node failures and chunk re-placement
type FailureHandler struct {
	engine            *PlacementEngine
	failedNodes       map[types.NodeID]time.Time
	chunksToReplace   map[string][]types.NodeID // chunkID -> failed nodes
}

// Rebalancer handles chunk rebalancing for optimal placement
type Rebalancer struct {
	engine            *PlacementEngine
	rebalanceInterval time.Duration
	lastRebalance     time.Time
}

// NewPlacementController creates a new placement controller
func NewPlacementController(localDomain string) *PlacementController {
	engine := NewPlacementEngine(localDomain)
	
	return &PlacementController{
		engine: engine,
		failureHandler: &FailureHandler{
			engine:          engine,
			failedNodes:     make(map[types.NodeID]time.Time),
			chunksToReplace: make(map[string][]types.NodeID),
		},
		rebalancer: &Rebalancer{
			engine:            engine,
			rebalanceInterval: 1 * time.Hour,
			lastRebalance:     time.Now(),
		},
	}
}

// ConfigureDataStore sets up placement policy for a DataStore
func (pc *PlacementController) ConfigureDataStore(path string, strategy PlacementStrategy, minReplicas, maxReplicas int) {
	policy := &ChunkPlacementPolicy{
		DataStore:   path,
		Strategy:    strategy,
		LocalFirst:  strategy == PlacementMedia,
		MinReplicas: minReplicas,
		MaxReplicas: maxReplicas,
		Constraints: pc.getDefaultConstraints(strategy),
	}
	
	pc.engine.SetPolicy(path, policy)
}

// PlaceChunk determines optimal placement for a new chunk
func (pc *PlacementController) PlaceChunk(chunkID string, chunkSize int64, datastorePath string) ([]types.NodeID, error) {
	// Get base replication factor
	replicas := pc.engine.GetReplicationFactor(chunkID, datastorePath)
	
	// Select nodes
	result, err := pc.engine.SelectNodes(datastorePath, chunkSize, replicas)
	if err != nil {
		return nil, fmt.Errorf("failed to select nodes: %w", err)
	}
	
	return result.Nodes, nil
}

// HandleNodeFailure processes a node failure and schedules re-placement
func (pc *PlacementController) HandleNodeFailure(nodeID types.NodeID, affectedChunks []string) error {
	pc.failureHandler.RecordFailure(nodeID)
	
	// Schedule chunk re-placement
	for _, chunkID := range affectedChunks {
		pc.failureHandler.ScheduleReplacement(chunkID, nodeID)
	}
	
	// Trigger immediate re-placement for critical chunks
	criticalChunks := pc.identifyCriticalChunks(affectedChunks)
	for _, chunkID := range criticalChunks {
		if err := pc.replaceChunk(chunkID, nodeID); err != nil {
			return fmt.Errorf("failed to replace critical chunk %s: %w", chunkID, err)
		}
	}
	
	return nil
}

// HandleNodeRecovery processes a node coming back online
func (pc *PlacementController) HandleNodeRecovery(nodeID types.NodeID) {
	pc.failureHandler.ClearFailure(nodeID)
	
	// Consider rebalancing to utilize recovered node
	pc.rebalancer.ScheduleRebalance()
}

// RecordAccess tracks chunk access for popularity-based replication
func (pc *PlacementController) RecordAccess(chunkID string) {
	pc.engine.TrackAccess(chunkID)
	
	// Check if chunk needs additional replicas due to popularity
	// This would be called periodically in production
}

// PerformRebalancing executes chunk rebalancing if needed
func (pc *PlacementController) PerformRebalancing() ([]RebalanceOperation, error) {
	if !pc.rebalancer.ShouldRebalance() {
		return nil, nil
	}
	
	operations := []RebalanceOperation{}
	
	// Get chunks that need rebalancing
	chunksToRebalance := pc.engine.RebalanceChunks()
	
	for _, chunkID := range chunksToRebalance {
		op := pc.planRebalanceOperation(chunkID)
		if op != nil {
			operations = append(operations, *op)
		}
	}
	
	pc.rebalancer.lastRebalance = time.Now()
	return operations, nil
}

// RebalanceOperation describes a chunk rebalancing operation
type RebalanceOperation struct {
	ChunkID     string
	FromNodes   []types.NodeID
	ToNodes     []types.NodeID
	Reason      string
}

// Helper methods for PlacementStrategy

func (pc *PlacementController) getDefaultConstraints(strategy PlacementStrategy) []string {
	switch strategy {
	case PlacementMedia:
		return []string{"low-latency", "high-bandwidth"}
	case PlacementBackup:
		return []string{"member-diverse", "rack-aware"}
	case PlacementHybrid:
		return []string{"balanced"}
	default:
		return []string{}
	}
}

func (pc *PlacementController) identifyCriticalChunks(chunks []string) []string {
	critical := []string{}
	
	// In production, this would check:
	// - Chunks with only 1 remaining replica
	// - Chunks from critical DataStores
	// - Recently accessed chunks
	
	// For now, consider first 10% as critical
	count := len(chunks) / 10
	if count < 1 && len(chunks) > 0 {
		count = 1
	}
	
	for i := 0; i < count && i < len(chunks); i++ {
		critical = append(critical, chunks[i])
	}
	
	return critical
}

func (pc *PlacementController) replaceChunk(chunkID string, failedNode types.NodeID) error {
	// In production, this would:
	// 1. Find current replica locations
	// 2. Select new node for replacement
	// 3. Initiate chunk transfer
	// 4. Update metadata
	
	// For now, just select a new node
	result, err := pc.engine.SelectNodes("/", 1024*1024, 1) // 1MB estimate
	if err != nil {
		return err
	}
	
	// Log the replacement
	fmt.Printf("Replacing chunk %s from failed node %s to %v\n", 
		chunkID, failedNode, result.Nodes)
	
	return nil
}

func (pc *PlacementController) planRebalanceOperation(chunkID string) *RebalanceOperation {
	// In production, this would analyze:
	// - Current placement vs optimal placement
	// - Node load balancing
	// - Network costs
	
	return &RebalanceOperation{
		ChunkID:   chunkID,
		FromNodes: []types.NodeID{}, // Would be filled with current nodes
		ToNodes:   []types.NodeID{}, // Would be filled with target nodes
		Reason:    "load balancing",
	}
}

// FailureHandler methods

func (fh *FailureHandler) RecordFailure(nodeID types.NodeID) {
	fh.failedNodes[nodeID] = time.Now()
	
	// Mark node as unhealthy in placement engine
	if node, exists := fh.engine.nodes[nodeID]; exists {
		node.Health = NodeUnhealthy
	}
}

func (fh *FailureHandler) ClearFailure(nodeID types.NodeID) {
	delete(fh.failedNodes, nodeID)
	
	// Mark node as healthy
	if node, exists := fh.engine.nodes[nodeID]; exists {
		node.Health = NodeHealthy
	}
	
	// Clear any pending replacements for this node
	for chunkID, nodes := range fh.chunksToReplace {
		newNodes := []types.NodeID{}
		for _, n := range nodes {
			if n != nodeID {
				newNodes = append(newNodes, n)
			}
		}
		if len(newNodes) == 0 {
			delete(fh.chunksToReplace, chunkID)
		} else {
			fh.chunksToReplace[chunkID] = newNodes
		}
	}
}

func (fh *FailureHandler) ScheduleReplacement(chunkID string, failedNode types.NodeID) {
	if nodes, exists := fh.chunksToReplace[chunkID]; exists {
		// Add to existing list if not already there
		found := false
		for _, n := range nodes {
			if n == failedNode {
				found = true
				break
			}
		}
		if !found {
			fh.chunksToReplace[chunkID] = append(nodes, failedNode)
		}
	} else {
		fh.chunksToReplace[chunkID] = []types.NodeID{failedNode}
	}
}

func (fh *FailureHandler) GetPendingReplacements() map[string][]types.NodeID {
	result := make(map[string][]types.NodeID)
	for k, v := range fh.chunksToReplace {
		result[k] = v
	}
	return result
}

// Rebalancer methods

func (r *Rebalancer) ShouldRebalance() bool {
	return time.Since(r.lastRebalance) >= r.rebalanceInterval
}

func (r *Rebalancer) ScheduleRebalance() {
	// In production, this would schedule an async rebalance task
	// For now, just reset the timer to trigger sooner
	r.lastRebalance = time.Now().Add(-r.rebalanceInterval).Add(5 * time.Minute)
}

// GetPlacementInfo returns information about current placement strategy
func (pc *PlacementController) GetPlacementInfo() map[string]interface{} {
	metrics := pc.engine.GetMetrics()
	
	return map[string]interface{}{
		"total_placements":       metrics.TotalPlacements,
		"local_placements":       metrics.LocalPlacements,
		"cross_member_placements": metrics.CrossMemberPlacements,
		"rebalance_operations":   metrics.RebalanceOperations,
		"failed_placements":      metrics.FailedPlacements,
		"failed_nodes":           len(pc.failureHandler.failedNodes),
		"pending_replacements":   len(pc.failureHandler.chunksToReplace),
		"last_rebalance":         pc.rebalancer.lastRebalance,
	}
}