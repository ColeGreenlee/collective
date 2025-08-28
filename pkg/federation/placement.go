package federation

import (
	"collective/pkg/types"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// ChunkPlacementPolicy defines how chunks should be placed across nodes
type ChunkPlacementPolicy struct {
	DataStore   string
	Strategy    PlacementStrategy
	LocalFirst  bool     // Prefer local nodes for media
	MinReplicas int      // Minimum replicas across members
	MaxReplicas int      // Maximum for popular content
	Constraints []string // rack-aware, member-diverse, etc.
}

// NodeInfo contains information about a storage node for placement decisions
type NodeInfo struct {
	NodeID       types.NodeID
	MemberDomain string
	Capacity     int64 // Total capacity in bytes
	Used         int64 // Used space in bytes
	Available    int64 // Available space in bytes
	Bandwidth    int64 // Bandwidth in bytes/sec
	Latency      time.Duration
	IsLocal      bool
	Labels       map[string]string // rack, datacenter, etc.
	Health       NodeHealth
	LoadScore    float64 // 0.0 (idle) to 1.0 (overloaded)
}

// NodeHealth represents the health status of a node
type NodeHealth int

const (
	NodeHealthy NodeHealth = iota
	NodeDegraded
	NodeUnhealthy
)

// PlacementEngine implements smart chunk placement strategies
type PlacementEngine struct {
	mu sync.RWMutex

	// Node information
	nodes map[types.NodeID]*NodeInfo

	// Placement policies by DataStore path
	policies map[string]*ChunkPlacementPolicy

	// Chunk popularity tracking
	chunkPopularity map[string]*ChunkPopularity

	// Local member domain
	localDomain string

	// Placement metrics
	metrics *PlacementMetrics
}

// ChunkPopularity tracks access patterns for a chunk
type ChunkPopularity struct {
	ChunkID      string
	AccessCount  int64
	LastAccessed time.Time
	HotScore     float64 // 0.0 (cold) to 1.0 (hot)
}

// PlacementMetrics tracks placement performance
type PlacementMetrics struct {
	TotalPlacements       int64
	LocalPlacements       int64
	CrossMemberPlacements int64
	RebalanceOperations   int64
	FailedPlacements      int64
}

// PlacementResult contains the result of a placement decision
type PlacementResult struct {
	Nodes       []types.NodeID
	Strategy    PlacementStrategy
	IsLocal     bool
	Constraints []string
}

// NewPlacementEngine creates a new placement engine
func NewPlacementEngine(localDomain string) *PlacementEngine {
	return &PlacementEngine{
		nodes:           make(map[types.NodeID]*NodeInfo),
		policies:        make(map[string]*ChunkPlacementPolicy),
		chunkPopularity: make(map[string]*ChunkPopularity),
		localDomain:     localDomain,
		metrics:         &PlacementMetrics{},
	}
}

// RegisterNode adds or updates node information
func (pe *PlacementEngine) RegisterNode(node *NodeInfo) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// Mark local nodes
	if node.MemberDomain == pe.localDomain {
		node.IsLocal = true
	}

	pe.nodes[node.NodeID] = node
}

// UnregisterNode removes a node from placement consideration
func (pe *PlacementEngine) UnregisterNode(nodeID types.NodeID) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	delete(pe.nodes, nodeID)
}

// SetPolicy sets the placement policy for a DataStore
func (pe *PlacementEngine) SetPolicy(datastorePath string, policy *ChunkPlacementPolicy) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.policies[datastorePath] = policy
}

// SelectNodes selects nodes for chunk placement based on strategy
func (pe *PlacementEngine) SelectNodes(datastorePath string, chunkSize int64, replicationFactor int) (*PlacementResult, error) {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	// Get policy for DataStore (or use default)
	policy := pe.getPolicy(datastorePath)

	// Get available nodes
	availableNodes := pe.getHealthyNodes(chunkSize)
	if len(availableNodes) < replicationFactor {
		pe.metrics.FailedPlacements++
		return nil, fmt.Errorf("insufficient healthy nodes: need %d, have %d",
			replicationFactor, len(availableNodes))
	}

	// Apply placement strategy
	var selectedNodes []types.NodeID
	switch policy.Strategy {
	case PlacementMedia:
		selectedNodes = pe.selectMediaNodes(availableNodes, replicationFactor, policy)
	case PlacementBackup:
		selectedNodes = pe.selectBackupNodes(availableNodes, replicationFactor, policy)
	case PlacementHybrid:
		selectedNodes = pe.selectHybridNodes(availableNodes, replicationFactor, policy)
	default:
		selectedNodes = pe.selectDefaultNodes(availableNodes, replicationFactor)
	}

	// Update metrics
	pe.updateMetrics(selectedNodes)

	return &PlacementResult{
		Nodes:       selectedNodes,
		Strategy:    policy.Strategy,
		IsLocal:     pe.isLocalPlacement(selectedNodes),
		Constraints: policy.Constraints,
	}, nil
}

// selectMediaNodes optimizes for streaming performance
func (pe *PlacementEngine) selectMediaNodes(nodes []*NodeInfo, count int, policy *ChunkPlacementPolicy) []types.NodeID {
	// Sort nodes by media suitability
	sort.Slice(nodes, func(i, j int) bool {
		// Prefer local nodes
		if policy.LocalFirst {
			if nodes[i].IsLocal != nodes[j].IsLocal {
				return nodes[i].IsLocal
			}
		}

		// Then by latency (lower is better)
		if nodes[i].Latency != nodes[j].Latency {
			return nodes[i].Latency < nodes[j].Latency
		}

		// Then by bandwidth (higher is better)
		if nodes[i].Bandwidth != nodes[j].Bandwidth {
			return nodes[i].Bandwidth > nodes[j].Bandwidth
		}

		// Finally by load (lower is better)
		return nodes[i].LoadScore < nodes[j].LoadScore
	})

	// Select top nodes
	selected := make([]types.NodeID, 0, count)
	for i := 0; i < count && i < len(nodes); i++ {
		selected = append(selected, nodes[i].NodeID)
	}

	return selected
}

// selectBackupNodes optimizes for durability and cross-member diversity
func (pe *PlacementEngine) selectBackupNodes(nodes []*NodeInfo, count int, policy *ChunkPlacementPolicy) []types.NodeID {
	// Group nodes by member domain
	memberNodes := make(map[string][]*NodeInfo)
	for _, node := range nodes {
		memberNodes[node.MemberDomain] = append(memberNodes[node.MemberDomain], node)
	}

	// Select nodes with member diversity
	selected := make([]types.NodeID, 0, count)
	membersUsed := make(map[string]bool)

	// First pass: one node per member
	for member, mnodes := range memberNodes {
		if len(selected) >= count {
			break
		}

		// Sort by capacity and health
		sort.Slice(mnodes, func(i, j int) bool {
			if mnodes[i].Health != mnodes[j].Health {
				return mnodes[i].Health < mnodes[j].Health
			}
			return mnodes[i].Available > mnodes[j].Available
		})

		if len(mnodes) > 0 {
			selected = append(selected, mnodes[0].NodeID)
			membersUsed[member] = true
		}
	}

	// Second pass: fill remaining slots
	if len(selected) < count {
		// Sort all nodes by available space
		sort.Slice(nodes, func(i, j int) bool {
			// Prefer nodes from unused members
			iUsed := membersUsed[nodes[i].MemberDomain]
			jUsed := membersUsed[nodes[j].MemberDomain]
			if iUsed != jUsed {
				return !iUsed // Prefer unused members
			}

			return nodes[i].Available > nodes[j].Available
		})

		for _, node := range nodes {
			if len(selected) >= count {
				break
			}

			// Check if node already selected
			alreadySelected := false
			for _, id := range selected {
				if id == node.NodeID {
					alreadySelected = true
					break
				}
			}

			if !alreadySelected {
				selected = append(selected, node.NodeID)
			}
		}
	}

	return selected
}

// selectHybridNodes balances between media and backup strategies
func (pe *PlacementEngine) selectHybridNodes(nodes []*NodeInfo, count int, policy *ChunkPlacementPolicy) []types.NodeID {
	// Calculate scores combining both strategies
	type scoredNode struct {
		node  *NodeInfo
		score float64
	}

	scoredNodes := make([]scoredNode, len(nodes))
	for i, node := range nodes {
		score := pe.calculateHybridScore(node, policy)
		scoredNodes[i] = scoredNode{node: node, score: score}
	}

	// Sort by score
	sort.Slice(scoredNodes, func(i, j int) bool {
		return scoredNodes[i].score > scoredNodes[j].score
	})

	// Select top nodes with some diversity
	selected := make([]types.NodeID, 0, count)
	membersUsed := make(map[string]int)

	for _, sn := range scoredNodes {
		if len(selected) >= count {
			break
		}

		// Limit nodes per member for diversity
		if membersUsed[sn.node.MemberDomain] >= 2 && len(selected) < count/2 {
			continue
		}

		selected = append(selected, sn.node.NodeID)
		membersUsed[sn.node.MemberDomain]++
	}

	return selected
}

// selectDefaultNodes uses basic selection
func (pe *PlacementEngine) selectDefaultNodes(nodes []*NodeInfo, count int) []types.NodeID {
	// Sort by available space and load
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].LoadScore != nodes[j].LoadScore {
			return nodes[i].LoadScore < nodes[j].LoadScore
		}
		return nodes[i].Available > nodes[j].Available
	})

	selected := make([]types.NodeID, 0, count)
	for i := 0; i < count && i < len(nodes); i++ {
		selected = append(selected, nodes[i].NodeID)
	}

	return selected
}

// calculateHybridScore calculates a combined score for hybrid placement
func (pe *PlacementEngine) calculateHybridScore(node *NodeInfo, policy *ChunkPlacementPolicy) float64 {
	score := 0.0

	// Factor 1: Available capacity (30%)
	capacityScore := float64(node.Available) / float64(node.Capacity)
	score += capacityScore * 0.3

	// Factor 2: Performance (30%)
	latencyScore := 1.0 - (float64(node.Latency) / float64(time.Second))
	if latencyScore < 0 {
		latencyScore = 0
	}
	bandwidthScore := float64(node.Bandwidth) / (1024 * 1024 * 1024) // Normalize to GB/s
	performanceScore := (latencyScore + bandwidthScore) / 2
	score += performanceScore * 0.3

	// Factor 3: Health and load (20%)
	healthScore := 0.0
	switch node.Health {
	case NodeHealthy:
		healthScore = 1.0
	case NodeDegraded:
		healthScore = 0.5
	case NodeUnhealthy:
		healthScore = 0.0
	}
	loadScore := 1.0 - node.LoadScore
	score += ((healthScore + loadScore) / 2) * 0.2

	// Factor 4: Locality bonus (20%)
	if policy.LocalFirst && node.IsLocal {
		score += 0.2
	}

	return score
}

// TrackAccess updates chunk popularity for replication decisions
func (pe *PlacementEngine) TrackAccess(chunkID string) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pop, exists := pe.chunkPopularity[chunkID]
	if !exists {
		pop = &ChunkPopularity{
			ChunkID: chunkID,
		}
		pe.chunkPopularity[chunkID] = pop
	}

	pop.AccessCount++
	pop.LastAccessed = time.Now()

	// Update hot score (exponential decay)
	timeSinceLastAccess := time.Since(pop.LastAccessed)
	decayFactor := math.Exp(-timeSinceLastAccess.Hours() / 24) // 24-hour half-life
	pop.HotScore = math.Min(1.0, pop.HotScore*decayFactor+0.1)
}

// GetReplicationFactor returns the replication factor for a chunk based on popularity
func (pe *PlacementEngine) GetReplicationFactor(chunkID string, datastorePath string) int {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	policy := pe.getPolicy(datastorePath)

	// Base replication
	replicas := policy.MinReplicas

	// Check popularity
	if pop, exists := pe.chunkPopularity[chunkID]; exists {
		if pop.HotScore > 0.7 {
			// Hot content gets more replicas
			replicas = policy.MaxReplicas
		} else if pop.HotScore > 0.4 {
			// Warm content gets medium replication
			replicas = (policy.MinReplicas + policy.MaxReplicas) / 2
		}
	}

	return replicas
}

// RebalanceChunks identifies chunks that need rebalancing
func (pe *PlacementEngine) RebalanceChunks() []string {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	needsRebalance := []string{}

	// Check each chunk's popularity
	for chunkID, pop := range pe.chunkPopularity {
		// Hot chunks that became cold
		if pop.HotScore < 0.3 && pop.AccessCount > 100 {
			timeSinceAccess := time.Since(pop.LastAccessed)
			if timeSinceAccess > 7*24*time.Hour {
				needsRebalance = append(needsRebalance, chunkID)
			}
		}
	}

	pe.metrics.RebalanceOperations++
	return needsRebalance
}

// Helper methods

func (pe *PlacementEngine) getPolicy(datastorePath string) *ChunkPlacementPolicy {
	if policy, exists := pe.policies[datastorePath]; exists {
		return policy
	}

	// Default policy
	return &ChunkPlacementPolicy{
		DataStore:   datastorePath,
		Strategy:    PlacementHybrid,
		LocalFirst:  false,
		MinReplicas: 2,
		MaxReplicas: 3,
		Constraints: []string{},
	}
}

func (pe *PlacementEngine) getHealthyNodes(minSpace int64) []*NodeInfo {
	nodes := []*NodeInfo{}
	for _, node := range pe.nodes {
		if node.Health != NodeUnhealthy && node.Available >= minSpace {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func (pe *PlacementEngine) isLocalPlacement(nodes []types.NodeID) bool {
	for _, nodeID := range nodes {
		if node, exists := pe.nodes[nodeID]; exists {
			if node.IsLocal {
				return true
			}
		}
	}
	return false
}

func (pe *PlacementEngine) updateMetrics(nodes []types.NodeID) {
	pe.metrics.TotalPlacements++

	localCount := 0
	memberDomains := make(map[string]bool)

	for _, nodeID := range nodes {
		if node, exists := pe.nodes[nodeID]; exists {
			if node.IsLocal {
				localCount++
			}
			memberDomains[node.MemberDomain] = true
		}
	}

	if localCount > 0 {
		pe.metrics.LocalPlacements++
	}

	if len(memberDomains) > 1 {
		pe.metrics.CrossMemberPlacements++
	}
}

// GetMetrics returns placement metrics
func (pe *PlacementEngine) GetMetrics() *PlacementMetrics {
	pe.mu.RLock()
	defer pe.mu.RUnlock()

	// Return a copy
	metrics := *pe.metrics
	return &metrics
}
