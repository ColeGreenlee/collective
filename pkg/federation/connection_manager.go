package federation

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"collective/pkg/protocol"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

// ConnectionManager manages federated connections with smart routing and failover
type ConnectionManager struct {
	localDomain string
	trustStore  *TrustStore
	pool        *ConnectionPool
	resilient   *ResilientClient
	gossip      *GossipService
	logger      *zap.Logger

	// Node preference tracking
	nodeScores map[string]*NodeScore // nodeID -> score
	scoreMu    sync.RWMutex

	// Health monitoring
	healthMonitor *HealthMonitor
	stopMonitor   chan struct{}
}

// NodeScore tracks the performance score of a node
type NodeScore struct {
	NodeID           string
	Domain           string
	Latency          time.Duration
	SuccessRate      float64
	LastUpdated      time.Time
	ConsecutiveFails int
	IsHealthy        bool
}

// HealthMonitor performs periodic health checks
type HealthMonitor struct {
	manager       *ConnectionManager
	checkInterval time.Duration
	mu            sync.Mutex
	lastCheck     time.Time
}

// NewConnectionManager creates a new connection manager
func NewConnectionManager(localDomain string, trustStore *TrustStore, gossip *GossipService, logger *zap.Logger) *ConnectionManager {
	if logger == nil {
		logger = zap.NewNop()
	}

	pool := NewConnectionPool(trustStore, "", "", logger)
	resilient := NewResilientClient(pool, gossip, logger)

	cm := &ConnectionManager{
		localDomain: localDomain,
		trustStore:  trustStore,
		pool:        pool,
		resilient:   resilient,
		gossip:      gossip,
		logger:      logger,
		nodeScores:  make(map[string]*NodeScore),
		stopMonitor: make(chan struct{}),
	}

	// Create health monitor
	cm.healthMonitor = &HealthMonitor{
		manager:       cm,
		checkInterval: 30 * time.Second,
	}

	// Start health monitoring
	go cm.healthMonitor.run(cm.stopMonitor)

	return cm
}

// GetConnection returns a connection based on the federated address
func (cm *ConnectionManager) GetConnection(addr *FederatedAddress) (*grpc.ClientConn, error) {
	if addr.IsLocal(cm.localDomain) {
		return cm.getLocalConnection(addr)
	}
	return cm.getFederatedConnection(addr)
}

// getLocalConnection returns a connection to a local node
func (cm *ConnectionManager) getLocalConnection(addr *FederatedAddress) (*grpc.ClientConn, error) {
	// For local connections, we can use a simpler approach
	endpoints := []string{"localhost:8001"} // Default local endpoint

	conn, err := cm.pool.GetConnection(cm.localDomain, endpoints)
	if err != nil {
		return nil, fmt.Errorf("failed to get local connection: %w", err)
	}

	return conn, nil
}

// getFederatedConnection returns a connection to a remote federation member
func (cm *ConnectionManager) getFederatedConnection(addr *FederatedAddress) (*grpc.ClientConn, error) {
	domain := addr.Domain

	// Get available nodes for this domain
	nodes := cm.getHealthyNodesForDomain(domain)
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no healthy nodes available for domain %s", domain)
	}

	// Sort nodes by score (best first)
	cm.sortNodesByScore(nodes)

	// Try nodes in order of preference
	var lastErr error
	for _, node := range nodes {
		endpoints := cm.getEndpointsForNode(node)
		if len(endpoints) == 0 {
			continue
		}

		conn, err := cm.pool.GetConnection(domain, endpoints)
		if err != nil {
			lastErr = err
			cm.recordFailure(node.NodeID)
			continue
		}

		// Success - record it
		cm.recordSuccess(node.NodeID)
		return conn, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to connect to domain %s: %w", domain, lastErr)
	}

	return nil, fmt.Errorf("no available endpoints for domain %s", domain)
}

// ExecuteWithFailover executes an RPC with automatic failover
func (cm *ConnectionManager) ExecuteWithFailover(
	ctx context.Context,
	addr *FederatedAddress,
	operation string,
	fn func(ctx context.Context, conn *grpc.ClientConn) error,
) error {
	domain := addr.Domain

	// Get all candidate nodes
	nodes := cm.getHealthyNodesForDomain(domain)
	if len(nodes) == 0 {
		return fmt.Errorf("no healthy nodes for domain %s", domain)
	}

	// Sort by score
	cm.sortNodesByScore(nodes)

	var lastErr error

	// Try each node with the resilient client
	for _, node := range nodes {
		// Record attempt
		cm.logger.Debug("Attempting operation on node",
			zap.String("operation", operation),
			zap.String("node", node.NodeID),
			zap.String("domain", domain))

		// Use resilient client for retry logic
		err := cm.resilient.CallWithRetry(ctx, domain, operation, fn)
		if err == nil {
			// Success!
			cm.recordSuccess(node.NodeID)
			return nil
		}

		// Record failure
		cm.recordFailure(node.NodeID)
		lastErr = err

		// Check if we should continue trying other nodes
		if !cm.resilient.isRetryableError(err) {
			// Non-retryable error, don't try other nodes
			return err
		}

		cm.logger.Warn("Failed on node, trying failover",
			zap.String("node", node.NodeID),
			zap.Error(err))
	}

	return fmt.Errorf("all nodes failed for operation %s: %w", operation, lastErr)
}

// getHealthyNodesForDomain returns healthy nodes for a domain
func (cm *ConnectionManager) getHealthyNodesForDomain(domain string) []*NodeScore {
	cm.scoreMu.RLock()
	defer cm.scoreMu.RUnlock()

	nodes := []*NodeScore{}

	// Get from node scores
	for _, score := range cm.nodeScores {
		if score.Domain == domain && score.IsHealthy {
			nodes = append(nodes, score)
		}
	}

	// If no scored nodes, create defaults from gossip
	if len(nodes) == 0 && cm.gossip != nil {
		peers := cm.gossip.GetHealthyPeers()
		for _, peer := range peers {
			if peer.Address.Domain == domain {
				score := &NodeScore{
					NodeID:      peer.Address.String(),
					Domain:      domain,
					SuccessRate: 0.5, // Unknown, start at 50%
					IsHealthy:   true,
					LastUpdated: time.Now(),
				}
				nodes = append(nodes, score)
			}
		}
	}

	return nodes
}

// sortNodesByScore sorts nodes by their performance score
func (cm *ConnectionManager) sortNodesByScore(nodes []*NodeScore) {
	sort.Slice(nodes, func(i, j int) bool {
		// First, prefer nodes with better success rate
		if nodes[i].SuccessRate != nodes[j].SuccessRate {
			return nodes[i].SuccessRate > nodes[j].SuccessRate
		}

		// Then, prefer nodes with lower latency
		if nodes[i].Latency != nodes[j].Latency {
			return nodes[i].Latency < nodes[j].Latency
		}

		// Finally, prefer nodes with fewer consecutive failures
		return nodes[i].ConsecutiveFails < nodes[j].ConsecutiveFails
	})
}

// getEndpointsForNode gets endpoints for a specific node
func (cm *ConnectionManager) getEndpointsForNode(node *NodeScore) []string {
	// Get from gossip if available
	if cm.gossip != nil {
		peers := cm.gossip.GetHealthyPeers()
		for _, peer := range peers {
			if peer.Address.String() == node.NodeID {
				endpoints := []string{}
				for _, ep := range peer.Endpoints {
					endpoints = append(endpoints, ep.DirectIPs...)
				}
				return endpoints
			}
		}
	}

	// Fallback to domain-based endpoint
	return []string{node.Domain + ":8001"}
}

// recordSuccess records a successful operation on a node
func (cm *ConnectionManager) recordSuccess(nodeID string) {
	cm.scoreMu.Lock()
	defer cm.scoreMu.Unlock()

	score, exists := cm.nodeScores[nodeID]
	if !exists {
		score = &NodeScore{
			NodeID:      nodeID,
			SuccessRate: 1.0,
		}
		cm.nodeScores[nodeID] = score
	}

	// Update success rate with exponential moving average
	alpha := 0.2 // Smoothing factor
	score.SuccessRate = alpha*1.0 + (1-alpha)*score.SuccessRate
	score.ConsecutiveFails = 0
	score.IsHealthy = true
	score.LastUpdated = time.Now()
}

// recordFailure records a failed operation on a node
func (cm *ConnectionManager) recordFailure(nodeID string) {
	cm.scoreMu.Lock()
	defer cm.scoreMu.Unlock()

	score, exists := cm.nodeScores[nodeID]
	if !exists {
		score = &NodeScore{
			NodeID:      nodeID,
			SuccessRate: 0,
		}
		cm.nodeScores[nodeID] = score
	}

	// Update success rate
	alpha := 0.2
	score.SuccessRate = alpha*0.0 + (1-alpha)*score.SuccessRate
	score.ConsecutiveFails++
	score.LastUpdated = time.Now()

	// Mark unhealthy after too many failures
	if score.ConsecutiveFails >= 3 {
		score.IsHealthy = false
		cm.logger.Warn("Node marked unhealthy",
			zap.String("node", nodeID),
			zap.Int("consecutive_failures", score.ConsecutiveFails))
	}
}

// UpdateNodeLatency updates the latency measurement for a node
func (cm *ConnectionManager) UpdateNodeLatency(nodeID string, latency time.Duration) {
	cm.scoreMu.Lock()
	defer cm.scoreMu.Unlock()

	score, exists := cm.nodeScores[nodeID]
	if !exists {
		score = &NodeScore{
			NodeID: nodeID,
		}
		cm.nodeScores[nodeID] = score
	}

	// Update latency with exponential moving average
	if score.Latency == 0 {
		score.Latency = latency
	} else {
		alpha := 0.3
		score.Latency = time.Duration(alpha*float64(latency) + (1-alpha)*float64(score.Latency))
	}

	score.LastUpdated = time.Now()
}

// GetStatistics returns connection manager statistics
func (cm *ConnectionManager) GetStatistics() map[string]interface{} {
	cm.scoreMu.RLock()
	defer cm.scoreMu.RUnlock()

	healthyNodes := 0
	unhealthyNodes := 0
	avgSuccessRate := 0.0

	for _, score := range cm.nodeScores {
		if score.IsHealthy {
			healthyNodes++
		} else {
			unhealthyNodes++
		}
		avgSuccessRate += score.SuccessRate
	}

	if len(cm.nodeScores) > 0 {
		avgSuccessRate /= float64(len(cm.nodeScores))
	}

	stats := map[string]interface{}{
		"total_nodes":      len(cm.nodeScores),
		"healthy_nodes":    healthyNodes,
		"unhealthy_nodes":  unhealthyNodes,
		"avg_success_rate": avgSuccessRate,
	}

	// Add resilient client stats
	if cm.resilient != nil {
		resilientStats := cm.resilient.GetStatistics()
		for k, v := range resilientStats {
			stats["resilient_"+k] = v
		}
	}

	return stats
}

// Close shuts down the connection manager
func (cm *ConnectionManager) Close() error {
	close(cm.stopMonitor)

	if cm.pool != nil {
		return cm.pool.Close()
	}

	return nil
}

// HealthMonitor implementation

func (hm *HealthMonitor) run(stop chan struct{}) {
	ticker := time.NewTicker(hm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			hm.performHealthChecks()

		case <-stop:
			return
		}
	}
}

func (hm *HealthMonitor) performHealthChecks() {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hm.lastCheck = time.Now()

	// Get all known domains from gossip
	if hm.manager.gossip == nil {
		return
	}

	peers := hm.manager.gossip.GetHealthyPeers()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, peer := range peers {
		domain := peer.Address.Domain

		// Perform health check
		start := time.Now()
		err := hm.manager.resilient.HealthCheck(ctx, domain)
		latency := time.Since(start)

		nodeID := peer.Address.String()

		if err == nil {
			// Healthy
			hm.manager.recordSuccess(nodeID)
			hm.manager.UpdateNodeLatency(nodeID, latency)

			hm.manager.logger.Debug("Health check passed",
				zap.String("node", nodeID),
				zap.Duration("latency", latency))
		} else {
			// Unhealthy
			hm.manager.recordFailure(nodeID)

			hm.manager.logger.Warn("Health check failed",
				zap.String("node", nodeID),
				zap.Error(err))
		}
	}
}

// CreateCoordinatorClient creates a coordinator client with automatic failover
func (cm *ConnectionManager) CreateCoordinatorClient(addr *FederatedAddress) (protocol.CoordinatorClient, error) {
	conn, err := cm.GetConnection(addr)
	if err != nil {
		return nil, err
	}

	return protocol.NewCoordinatorClient(conn), nil
}
