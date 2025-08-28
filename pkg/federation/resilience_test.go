package federation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

// Mock connection for testing - embeds real grpc.ClientConn for interface compliance
type mockConnWrapper struct {
	*grpc.ClientConn
	state connectivity.State
	mu    sync.RWMutex
}

func (m *mockConnWrapper) GetState() connectivity.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *mockConnWrapper) setState(state connectivity.State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
}

func (m *mockConnWrapper) Close() error {
	if m.ClientConn != nil {
		return m.ClientConn.Close()
	}
	return nil
}

// Test retry logic with exponential backoff
func TestResilientClient_RetryWithBackoff(t *testing.T) {
	logger := zap.NewNop()
	pool := &ConnectionPool{
		connections: make(map[string]*PooledConnection),
		logger:      logger,
	}

	client := NewResilientClient(pool, nil, logger)
	client.maxRetries = 3
	client.baseDelay = 10 * time.Millisecond
	client.maxDelay = 100 * time.Millisecond

	// Track retry attempts
	attemptCount := int32(0)

	// Create a function that fails first 2 times, then succeeds
	fn := func(ctx context.Context, conn *grpc.ClientConn) error {
		attempt := atomic.AddInt32(&attemptCount, 1)
		if attempt < 3 {
			// Return retryable error
			return status.Error(codes.Unavailable, "service unavailable")
		}
		return nil // Success on third attempt
	}

	ctx := context.Background()
	start := time.Now()

	// Mock connection
	conn := &grpc.ClientConn{}
	err := client.retryOperation(ctx, conn, "test-domain", "test-op", fn, 0)

	elapsed := time.Since(start)

	// Should succeed after retries
	if err != nil {
		t.Errorf("Expected success after retries, got error: %v", err)
	}

	// Should have attempted 3 times
	if attemptCount != 3 {
		t.Errorf("Expected 3 attempts, got %d", attemptCount)
	}

	// Should have taken at least baseDelay * 2.5 due to backoff (with some tolerance)
	// (no delay after last attempt)
	minExpectedDelay := time.Duration(float64(client.baseDelay) * 2.5) // Approximate due to exponential backoff and jitter
	if elapsed < minExpectedDelay {
		t.Errorf("Expected at least %v delay, got %v", minExpectedDelay, elapsed)
	}
}

// Test non-retryable errors
func TestResilientClient_NonRetryableError(t *testing.T) {
	logger := zap.NewNop()
	pool := &ConnectionPool{
		connections: make(map[string]*PooledConnection),
		logger:      logger,
	}

	client := NewResilientClient(pool, nil, logger)

	attemptCount := int32(0)

	// Return non-retryable error
	fn := func(ctx context.Context, conn *grpc.ClientConn) error {
		atomic.AddInt32(&attemptCount, 1)
		return status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := context.Background()
	conn := &grpc.ClientConn{}
	err := client.retryOperation(ctx, conn, "test-domain", "test-op", fn, 0)

	// Should fail immediately
	if err == nil {
		t.Error("Expected error for non-retryable error")
	}

	// Should only attempt once
	if attemptCount != 1 {
		t.Errorf("Expected 1 attempt for non-retryable error, got %d", attemptCount)
	}
}

// Test circuit breaker
func TestConnectionPool_CircuitBreaker(t *testing.T) {
	logger := zap.NewNop()
	trustStore := &TrustStore{}
	pool := NewConnectionPool(trustStore, "", "", logger)
	defer pool.Close()

	// Create a pooled connection (conn can be nil for this test)
	pooled := &PooledConnection{
		conn:         nil, // OK for circuit breaker test
		domain:       "test-domain",
		created:      time.Now(),
		lastUsed:     time.Now(),
		isHealthy:    true,
		circuitState: CircuitClosed,
	}

	pool.mu.Lock()
	pool.connections["test-domain"] = pooled
	pool.mu.Unlock()

	// Mark unhealthy multiple times to trigger circuit breaker
	pool.MarkUnhealthy("test-domain")
	pool.MarkUnhealthy("test-domain")
	pool.MarkUnhealthy("test-domain")

	// Circuit should be open
	if pooled.circuitState != CircuitOpen {
		t.Error("Circuit breaker should be open after 3 failures")
	}

	// Connection should not be usable
	if pooled.isUsable() {
		t.Error("Connection should not be usable when circuit is open")
	}
}

// Test connection pool maintenance
func TestConnectionPool_Maintenance(t *testing.T) {
	logger := zap.NewNop()
	trustStore := &TrustStore{}
	pool := NewConnectionPool(trustStore, "", "", logger)
	pool.idleTimeout = 100 * time.Millisecond
	defer pool.Close()

	// Create an idle connection
	pooled := &PooledConnection{
		conn:      nil, // OK for maintenance test
		domain:    "test-domain",
		created:   time.Now().Add(-1 * time.Hour),
		lastUsed:  time.Now().Add(-1 * time.Hour), // Very old
		isHealthy: true,
	}

	pool.mu.Lock()
	pool.connections["test-domain"] = pooled
	pool.mu.Unlock()

	// Trigger maintenance
	pool.performMaintenance()

	// Connection should be removed
	pool.mu.RLock()
	_, exists := pool.connections["test-domain"]
	pool.mu.RUnlock()

	if exists {
		t.Error("Idle connection should have been removed")
	}
}

// Test failover mechanism
func TestConnectionManager_Failover(t *testing.T) {
	logger := zap.NewNop()
	trustStore := &TrustStore{}
	gossip, _ := NewGossipService("test.local", nil, "", "")

	manager := NewConnectionManager("test.local", trustStore, gossip, logger)
	defer manager.Close()

	// Add multiple nodes with different scores
	manager.nodeScores["node1"] = &NodeScore{
		NodeID:           "node1",
		Domain:           "remote.local",
		SuccessRate:      0.9,
		IsHealthy:        true,
		ConsecutiveFails: 0,
	}

	manager.nodeScores["node2"] = &NodeScore{
		NodeID:           "node2",
		Domain:           "remote.local",
		SuccessRate:      0.8,
		IsHealthy:        true,
		ConsecutiveFails: 0,
	}

	manager.nodeScores["node3"] = &NodeScore{
		NodeID:           "node3",
		Domain:           "remote.local",
		SuccessRate:      0.7,
		IsHealthy:        true,
		ConsecutiveFails: 0,
	}

	// Get healthy nodes
	nodes := manager.getHealthyNodesForDomain("remote.local")

	if len(nodes) != 3 {
		t.Errorf("Expected 3 healthy nodes, got %d", len(nodes))
	}

	// Sort by score
	manager.sortNodesByScore(nodes)

	// Should be ordered by success rate
	if nodes[0].NodeID != "node1" {
		t.Errorf("Expected node1 first, got %s", nodes[0].NodeID)
	}
	if nodes[1].NodeID != "node2" {
		t.Errorf("Expected node2 second, got %s", nodes[1].NodeID)
	}
	if nodes[2].NodeID != "node3" {
		t.Errorf("Expected node3 third, got %s", nodes[2].NodeID)
	}
}

// Test node scoring updates
func TestConnectionManager_NodeScoring(t *testing.T) {
	logger := zap.NewNop()
	trustStore := &TrustStore{}
	gossip, _ := NewGossipService("test.local", nil, "", "")

	manager := NewConnectionManager("test.local", trustStore, gossip, logger)
	defer manager.Close()

	// Record successes and failures
	nodeID := "test-node"

	// Start with no score
	manager.recordSuccess(nodeID)

	score := manager.nodeScores[nodeID]
	if score.SuccessRate != 1.0 {
		t.Errorf("Expected success rate 1.0 after first success, got %f", score.SuccessRate)
	}

	// Record failure
	manager.recordFailure(nodeID)

	// Success rate should decrease
	if score.SuccessRate >= 1.0 {
		t.Error("Success rate should decrease after failure")
	}

	// Record multiple failures
	manager.recordFailure(nodeID)
	manager.recordFailure(nodeID)

	// Should be marked unhealthy
	if score.IsHealthy {
		t.Error("Node should be unhealthy after 3 consecutive failures")
	}
}

// Test latency tracking
func TestConnectionManager_LatencyTracking(t *testing.T) {
	logger := zap.NewNop()
	trustStore := &TrustStore{}
	gossip, _ := NewGossipService("test.local", nil, "", "")

	manager := NewConnectionManager("test.local", trustStore, gossip, logger)
	defer manager.Close()

	nodeID := "test-node"

	// Update latency multiple times
	manager.UpdateNodeLatency(nodeID, 10*time.Millisecond)
	manager.UpdateNodeLatency(nodeID, 20*time.Millisecond)
	manager.UpdateNodeLatency(nodeID, 30*time.Millisecond)

	score := manager.nodeScores[nodeID]

	// Latency should be averaged
	if score.Latency == 0 {
		t.Error("Latency should be tracked")
	}

	// Should be between min and max due to exponential moving average
	if score.Latency < 10*time.Millisecond || score.Latency > 30*time.Millisecond {
		t.Errorf("Latency %v outside expected range", score.Latency)
	}
}

// Test backoff calculation
func TestResilientClient_BackoffCalculation(t *testing.T) {
	client := &ResilientClient{
		baseDelay:    100 * time.Millisecond,
		maxDelay:     5 * time.Second,
		jitterFactor: 0.2,
	}

	// Test exponential growth
	delays := []time.Duration{}
	for i := 0; i < 5; i++ {
		delay := client.calculateBackoff(i)
		delays = append(delays, delay)

		// Should not exceed max delay
		if delay > client.maxDelay {
			t.Errorf("Delay %v exceeds max delay %v", delay, client.maxDelay)
		}
	}

	// Each delay should be roughly double the previous (minus jitter)
	for i := 1; i < len(delays)-1; i++ {
		ratio := float64(delays[i]) / float64(delays[i-1])
		// Allow for jitter - ratio should be roughly 2.0, but with 20% jitter it can vary
		if ratio < 1.3 || ratio > 2.7 {
			// Skip check for capped values
			if delays[i] < client.maxDelay && delays[i-1] < client.maxDelay {
				t.Errorf("Unexpected backoff ratio %f at iteration %d", ratio, i)
			}
		}
	}
}

// Test statistics collection
func TestConnectionManager_Statistics(t *testing.T) {
	logger := zap.NewNop()
	trustStore := &TrustStore{}
	gossip, _ := NewGossipService("test.local", nil, "", "")

	manager := NewConnectionManager("test.local", trustStore, gossip, logger)
	defer manager.Close()

	// Add some nodes with different states
	manager.nodeScores["healthy1"] = &NodeScore{
		IsHealthy:   true,
		SuccessRate: 0.9,
	}
	manager.nodeScores["healthy2"] = &NodeScore{
		IsHealthy:   true,
		SuccessRate: 0.8,
	}
	manager.nodeScores["unhealthy1"] = &NodeScore{
		IsHealthy:   false,
		SuccessRate: 0.2,
	}

	stats := manager.GetStatistics()

	// Check statistics
	if stats["total_nodes"] != 3 {
		t.Errorf("Expected 3 total nodes, got %v", stats["total_nodes"])
	}
	if stats["healthy_nodes"] != 2 {
		t.Errorf("Expected 2 healthy nodes, got %v", stats["healthy_nodes"])
	}
	if stats["unhealthy_nodes"] != 1 {
		t.Errorf("Expected 1 unhealthy node, got %v", stats["unhealthy_nodes"])
	}

	avgRate := stats["avg_success_rate"].(float64)
	expectedAvg := (0.9 + 0.8 + 0.2) / 3
	if avgRate < expectedAvg-0.01 || avgRate > expectedAvg+0.01 {
		t.Errorf("Expected average success rate ~%f, got %f", expectedAvg, avgRate)
	}
}
