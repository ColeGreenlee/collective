package federation

import (
	"collective/pkg/types"
	"testing"
	"time"
)

func createTestNodes() []*NodeInfo {
	return []*NodeInfo{
		// Local nodes (alice)
		{
			NodeID:       "alice-node-01",
			MemberDomain: "alice.collective.local",
			Capacity:     10 * 1024 * 1024 * 1024, // 10GB
			Used:         2 * 1024 * 1024 * 1024,  // 2GB
			Available:    8 * 1024 * 1024 * 1024,  // 8GB
			Bandwidth:    1024 * 1024 * 1024,      // 1GB/s
			Latency:      1 * time.Millisecond,
			IsLocal:      true,
			Health:       NodeHealthy,
			LoadScore:    0.2,
		},
		{
			NodeID:       "alice-node-02",
			MemberDomain: "alice.collective.local",
			Capacity:     10 * 1024 * 1024 * 1024,
			Used:         5 * 1024 * 1024 * 1024,
			Available:    5 * 1024 * 1024 * 1024,
			Bandwidth:    500 * 1024 * 1024, // 500MB/s
			Latency:      2 * time.Millisecond,
			IsLocal:      true,
			Health:       NodeHealthy,
			LoadScore:    0.5,
		},
		// Remote nodes (bob)
		{
			NodeID:       "bob-node-01",
			MemberDomain: "bob.collective.local",
			Capacity:     20 * 1024 * 1024 * 1024, // 20GB
			Used:         5 * 1024 * 1024 * 1024,
			Available:    15 * 1024 * 1024 * 1024,
			Bandwidth:    2 * 1024 * 1024 * 1024, // 2GB/s
			Latency:      10 * time.Millisecond,
			IsLocal:      false,
			Health:       NodeHealthy,
			LoadScore:    0.25,
		},
		{
			NodeID:       "bob-node-02",
			MemberDomain: "bob.collective.local",
			Capacity:     20 * 1024 * 1024 * 1024,
			Used:         10 * 1024 * 1024 * 1024,
			Available:    10 * 1024 * 1024 * 1024,
			Bandwidth:    1024 * 1024 * 1024,
			Latency:      12 * time.Millisecond,
			IsLocal:      false,
			Health:       NodeDegraded,
			LoadScore:    0.5,
		},
		// Remote nodes (carol)
		{
			NodeID:       "carol-node-01",
			MemberDomain: "carol.collective.local",
			Capacity:     30 * 1024 * 1024 * 1024, // 30GB
			Used:         10 * 1024 * 1024 * 1024,
			Available:    20 * 1024 * 1024 * 1024,
			Bandwidth:    3 * 1024 * 1024 * 1024, // 3GB/s
			Latency:      20 * time.Millisecond,
			IsLocal:      false,
			Health:       NodeHealthy,
			LoadScore:    0.33,
		},
	}
}

func TestPlacementEngine_RegisterNodes(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	nodes := createTestNodes()
	
	// Register nodes
	for _, node := range nodes {
		pe.RegisterNode(node)
	}
	
	// Check nodes were registered
	if len(pe.nodes) != len(nodes) {
		t.Errorf("Expected %d nodes, got %d", len(nodes), len(pe.nodes))
	}
	
	// Check local nodes are marked correctly
	aliceNode1 := pe.nodes["alice-node-01"]
	if !aliceNode1.IsLocal {
		t.Error("alice-node-01 should be marked as local")
	}
	
	bobNode1 := pe.nodes["bob-node-01"]
	if bobNode1.IsLocal {
		t.Error("bob-node-01 should not be marked as local")
	}
}

func TestPlacementEngine_MediaStrategy(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	nodes := createTestNodes()
	
	// Register nodes
	for _, node := range nodes {
		pe.RegisterNode(node)
	}
	
	// Set media policy
	policy := &ChunkPlacementPolicy{
		DataStore:   "/media",
		Strategy:    PlacementMedia,
		LocalFirst:  true,
		MinReplicas: 2,
		MaxReplicas: 3,
	}
	pe.SetPolicy("/media", policy)
	
	// Select nodes for media chunk
	result, err := pe.SelectNodes("/media", 1024*1024, 2) // 1MB chunk, 2 replicas
	if err != nil {
		t.Fatalf("Failed to select nodes: %v", err)
	}
	
	if len(result.Nodes) != 2 {
		t.Errorf("Expected 2 nodes, got %d", len(result.Nodes))
	}
	
	// Should prefer local nodes with low latency
	// alice-node-01 should be first (local, lowest latency, high bandwidth)
	if result.Nodes[0] != "alice-node-01" {
		t.Errorf("Expected alice-node-01 as first node for media, got %s", result.Nodes[0])
	}
	
	// Should be local placement
	if !result.IsLocal {
		t.Error("Media placement should be local")
	}
}

func TestPlacementEngine_BackupStrategy(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	nodes := createTestNodes()
	
	// Register nodes
	for _, node := range nodes {
		pe.RegisterNode(node)
	}
	
	// Set backup policy
	policy := &ChunkPlacementPolicy{
		DataStore:   "/backups",
		Strategy:    PlacementBackup,
		LocalFirst:  false,
		MinReplicas: 3,
		MaxReplicas: 5,
	}
	pe.SetPolicy("/backups", policy)
	
	// Select nodes for backup chunk
	result, err := pe.SelectNodes("/backups", 1024*1024, 3) // 1MB chunk, 3 replicas
	if err != nil {
		t.Fatalf("Failed to select nodes: %v", err)
	}
	
	if len(result.Nodes) != 3 {
		t.Errorf("Expected 3 nodes, got %d", len(result.Nodes))
	}
	
	// Should have cross-member diversity
	memberDomains := make(map[string]bool)
	for _, nodeID := range result.Nodes {
		if node, exists := pe.nodes[nodeID]; exists {
			memberDomains[node.MemberDomain] = true
		}
	}
	
	// Should use multiple member domains for backup
	if len(memberDomains) < 2 {
		t.Errorf("Backup should use multiple members, got %d", len(memberDomains))
	}
}

func TestPlacementEngine_HybridStrategy(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	nodes := createTestNodes()
	
	// Register nodes
	for _, node := range nodes {
		pe.RegisterNode(node)
	}
	
	// Set hybrid policy
	policy := &ChunkPlacementPolicy{
		DataStore:   "/shared",
		Strategy:    PlacementHybrid,
		LocalFirst:  false,
		MinReplicas: 2,
		MaxReplicas: 4,
	}
	pe.SetPolicy("/shared", policy)
	
	// Select nodes for hybrid chunk
	result, err := pe.SelectNodes("/shared", 1024*1024, 2)
	if err != nil {
		t.Fatalf("Failed to select nodes: %v", err)
	}
	
	if len(result.Nodes) != 2 {
		t.Errorf("Expected 2 nodes, got %d", len(result.Nodes))
	}
	
	// Hybrid should balance various factors
	// Should select nodes with good combined score
	for _, nodeID := range result.Nodes {
		node := pe.nodes[nodeID]
		if node.Health == NodeUnhealthy {
			t.Error("Should not select unhealthy nodes")
		}
	}
}

func TestPlacementEngine_PopularityTracking(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	
	// Track access for a chunk
	chunkID := "chunk-001"
	
	// Simulate multiple accesses
	for i := 0; i < 10; i++ {
		pe.TrackAccess(chunkID)
	}
	
	// Check popularity was tracked
	if pop, exists := pe.chunkPopularity[chunkID]; exists {
		if pop.AccessCount != 10 {
			t.Errorf("Expected 10 accesses, got %d", pop.AccessCount)
		}
		
		if pop.HotScore <= 0 {
			t.Error("Hot score should be positive after accesses")
		}
	} else {
		t.Error("Chunk popularity not tracked")
	}
	
	// Check replication factor increases for hot content
	policy := &ChunkPlacementPolicy{
		MinReplicas: 2,
		MaxReplicas: 5,
	}
	pe.SetPolicy("/data", policy)
	
	replicas := pe.GetReplicationFactor(chunkID, "/data")
	if replicas <= 2 {
		t.Error("Hot content should have more than minimum replicas")
	}
}

func TestPlacementEngine_InsufficientNodes(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	
	// Register only one node
	node := &NodeInfo{
		NodeID:       "single-node",
		MemberDomain: "alice.collective.local",
		Capacity:     10 * 1024 * 1024 * 1024,
		Available:    5 * 1024 * 1024 * 1024,
		Health:       NodeHealthy,
		IsLocal:      true,
	}
	pe.RegisterNode(node)
	
	// Try to select 3 replicas with only 1 node
	_, err := pe.SelectNodes("/data", 1024*1024, 3)
	if err == nil {
		t.Error("Expected error with insufficient nodes")
	}
	
	// Check metrics
	if pe.metrics.FailedPlacements != 1 {
		t.Errorf("Expected 1 failed placement, got %d", pe.metrics.FailedPlacements)
	}
}

func TestPlacementController_NodeFailure(t *testing.T) {
	pc := NewPlacementController("alice.collective.local")
	nodes := createTestNodes()
	
	// Register nodes
	for _, node := range nodes {
		pc.engine.RegisterNode(node)
	}
	
	// Handle node failure
	failedNode := types.NodeID("alice-node-01")
	affectedChunks := []string{"chunk-001", "chunk-002", "chunk-003"}
	
	err := pc.HandleNodeFailure(failedNode, affectedChunks)
	if err != nil {
		t.Errorf("Failed to handle node failure: %v", err)
	}
	
	// Check node marked as failed
	if _, exists := pc.failureHandler.failedNodes[failedNode]; !exists {
		t.Error("Failed node not recorded")
	}
	
	// Check chunks scheduled for replacement
	pendingReplacements := pc.failureHandler.GetPendingReplacements()
	if len(pendingReplacements) == 0 {
		t.Error("No chunks scheduled for replacement")
	}
}

func TestPlacementController_NodeRecovery(t *testing.T) {
	pc := NewPlacementController("alice.collective.local")
	
	// First mark node as failed
	failedNode := types.NodeID("alice-node-01")
	pc.failureHandler.RecordFailure(failedNode)
	
	// Then recover it
	pc.HandleNodeRecovery(failedNode)
	
	// Check node no longer marked as failed
	if _, exists := pc.failureHandler.failedNodes[failedNode]; exists {
		t.Error("Recovered node should not be in failed list")
	}
}

func TestPlacementEngine_Metrics(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	nodes := createTestNodes()
	
	// Register nodes
	for _, node := range nodes {
		pe.RegisterNode(node)
	}
	
	// Set a policy that prefers local nodes
	policy := &ChunkPlacementPolicy{
		DataStore:   "/data",
		Strategy:    PlacementHybrid,
		LocalFirst:  true, // This will give local nodes a bonus
		MinReplicas: 2,
		MaxReplicas: 3,
	}
	pe.SetPolicy("/data", policy)
	
	// Perform several placements
	for i := 0; i < 5; i++ {
		pe.SelectNodes("/data", 1024*1024, 2)
	}
	
	metrics := pe.GetMetrics()
	if metrics.TotalPlacements != 5 {
		t.Errorf("Expected 5 total placements, got %d", metrics.TotalPlacements)
	}
	
	if metrics.LocalPlacements == 0 {
		t.Error("Expected some local placements")
	}
}

func TestPlacementEngine_UnregisterNode(t *testing.T) {
	pe := NewPlacementEngine("alice.collective.local")
	
	// Register and then unregister a node
	node := &NodeInfo{
		NodeID:       "temp-node",
		MemberDomain: "alice.collective.local",
		Health:       NodeHealthy,
	}
	
	pe.RegisterNode(node)
	if len(pe.nodes) != 1 {
		t.Error("Node not registered")
	}
	
	pe.UnregisterNode("temp-node")
	if len(pe.nodes) != 0 {
		t.Error("Node not unregistered")
	}
}

func TestRebalancer_ShouldRebalance(t *testing.T) {
	pc := NewPlacementController("alice.collective.local")
	
	// Initially should not need rebalance
	if pc.rebalancer.ShouldRebalance() {
		t.Error("Should not need rebalance immediately")
	}
	
	// Simulate time passing
	pc.rebalancer.lastRebalance = time.Now().Add(-2 * time.Hour)
	
	if !pc.rebalancer.ShouldRebalance() {
		t.Error("Should need rebalance after interval")
	}
	
	// Schedule rebalance
	pc.rebalancer.ScheduleRebalance()
	
	// Should be scheduled soon
	timeTillRebalance := pc.rebalancer.lastRebalance.Add(pc.rebalancer.rebalanceInterval).Sub(time.Now())
	if timeTillRebalance > 10*time.Minute {
		t.Error("Rebalance not scheduled soon enough")
	}
}