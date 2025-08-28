package federation

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

// ConnectionPool manages gRPC connections to federation members
type ConnectionPool struct {
	mu          sync.RWMutex
	connections map[string]*PooledConnection // domain -> connection
	trustStore  *TrustStore
	logger      *zap.Logger

	// Client certificate paths for mutual authentication
	certPath string
	keyPath  string

	// Configuration
	maxIdleConns        int
	maxConnsPerHost     int
	idleTimeout         time.Duration
	healthCheckInterval time.Duration

	// Cleanup
	stopCleanup chan struct{}
}

// PooledConnection wraps a gRPC connection with metadata
type PooledConnection struct {
	conn      *grpc.ClientConn
	domain    string
	created   time.Time
	lastUsed  time.Time
	useCount  int64
	isHealthy bool
	mu        sync.RWMutex

	// Circuit breaker state
	failures     int
	lastFailure  time.Time
	circuitState CircuitState
}

// CircuitState represents the circuit breaker state
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // Normal operation
	CircuitOpen                         // Failing, reject requests
	CircuitHalfOpen                     // Testing recovery
)

// NewConnectionPool creates a new connection pool
func NewConnectionPool(trustStore *TrustStore, certPath, keyPath string, logger *zap.Logger) *ConnectionPool {
	if logger == nil {
		logger = zap.NewNop()
	}

	cp := &ConnectionPool{
		connections:         make(map[string]*PooledConnection),
		trustStore:          trustStore,
		certPath:            certPath,
		keyPath:             keyPath,
		logger:              logger,
		maxIdleConns:        10,
		maxConnsPerHost:     3,
		idleTimeout:         5 * time.Minute,
		healthCheckInterval: 30 * time.Second,
		stopCleanup:         make(chan struct{}),
	}

	// Start background maintenance
	go cp.maintainConnections()

	return cp
}

// GetConnection returns a connection to the specified domain
func (cp *ConnectionPool) GetConnection(domain string, endpoints []string) (*grpc.ClientConn, error) {
	cp.mu.RLock()
	pooled, exists := cp.connections[domain]
	cp.mu.RUnlock()

	if exists && pooled.isUsable() {
		pooled.recordUse()
		return pooled.conn, nil
	}

	// Need to create a new connection
	return cp.createConnection(domain, endpoints)
}

// createConnection creates a new connection to a domain
func (cp *ConnectionPool) createConnection(domain string, endpoints []string) (*grpc.ClientConn, error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Check again after acquiring write lock
	if pooled, exists := cp.connections[domain]; exists && pooled.isUsable() {
		pooled.recordUse()
		return pooled.conn, nil
	}

	// Try each endpoint until one works
	var lastErr error
	for _, endpoint := range endpoints {
		conn, err := cp.dialEndpoint(domain, endpoint)
		if err != nil {
			lastErr = err
			cp.logger.Debug("Failed to connect to endpoint",
				zap.String("domain", domain),
				zap.String("endpoint", endpoint),
				zap.Error(err))
			continue
		}

		// Successfully connected
		pooled := &PooledConnection{
			conn:         conn,
			domain:       domain,
			created:      time.Now(),
			lastUsed:     time.Now(),
			isHealthy:    true,
			circuitState: CircuitClosed,
		}

		cp.connections[domain] = pooled
		cp.logger.Info("Established connection to federation member",
			zap.String("domain", domain),
			zap.String("endpoint", endpoint))

		return conn, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("failed to connect to any endpoint for %s: %w", domain, lastErr)
	}

	return nil, fmt.Errorf("no endpoints available for %s", domain)
}

// dialEndpoint creates a gRPC connection to a specific endpoint
func (cp *ConnectionPool) dialEndpoint(domain string, endpoint string) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get TLS config from trust store with client certificates
	tlsConfig, err := cp.trustStore.GetClientTLSConfig(endpoint, cp.certPath, cp.keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get TLS config: %w", err)
	}

	// Create connection with TLS
	creds := credentials.NewTLS(tlsConfig)
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
	}

	conn, err := grpc.DialContext(ctx, endpoint, opts...)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// ReleaseConnection returns a connection to the pool
func (cp *ConnectionPool) ReleaseConnection(domain string, conn *grpc.ClientConn) {
	cp.mu.RLock()
	pooled, exists := cp.connections[domain]
	cp.mu.RUnlock()

	if !exists || pooled.conn != conn {
		// Not our connection, close it
		conn.Close()
		return
	}

	// Connection stays in pool for reuse
	pooled.recordUse()
}

// MarkUnhealthy marks a connection as unhealthy
func (cp *ConnectionPool) MarkUnhealthy(domain string) {
	cp.mu.RLock()
	pooled, exists := cp.connections[domain]
	cp.mu.RUnlock()

	if !exists {
		return
	}

	pooled.mu.Lock()
	defer pooled.mu.Unlock()

	pooled.failures++
	pooled.lastFailure = time.Now()

	// Circuit breaker logic
	if pooled.failures >= 3 {
		pooled.circuitState = CircuitOpen
		cp.logger.Warn("Circuit breaker opened for domain",
			zap.String("domain", domain),
			zap.Int("failures", pooled.failures))
	}
}

// maintainConnections performs periodic maintenance
func (cp *ConnectionPool) maintainConnections() {
	ticker := time.NewTicker(cp.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cp.performMaintenance()

		case <-cp.stopCleanup:
			return
		}
	}
}

// performMaintenance cleans up idle connections and checks health
func (cp *ConnectionPool) performMaintenance() {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	now := time.Now()
	toRemove := []string{}

	for domain, pooled := range cp.connections {
		pooled.mu.RLock()
		idleTime := now.Sub(pooled.lastUsed)
		var state connectivity.State
		if pooled.conn != nil {
			state = pooled.conn.GetState()
		} else {
			state = connectivity.Shutdown
		}
		pooled.mu.RUnlock()

		// Remove idle connections
		if idleTime > cp.idleTimeout {
			toRemove = append(toRemove, domain)
			continue
		}

		// Check connection health
		if state == connectivity.TransientFailure || state == connectivity.Shutdown {
			pooled.mu.Lock()
			pooled.isHealthy = false
			pooled.mu.Unlock()
		}

		// Reset circuit breaker after cooldown
		if pooled.circuitState == CircuitOpen && now.Sub(pooled.lastFailure) > 30*time.Second {
			pooled.mu.Lock()
			pooled.circuitState = CircuitHalfOpen
			pooled.failures = 0
			pooled.mu.Unlock()

			cp.logger.Info("Circuit breaker moved to half-open",
				zap.String("domain", domain))
		}
	}

	// Remove idle connections
	for _, domain := range toRemove {
		if pooled, exists := cp.connections[domain]; exists {
			if pooled.conn != nil {
				pooled.conn.Close()
			}
			delete(cp.connections, domain)
			cp.logger.Debug("Removed idle connection",
				zap.String("domain", domain))
		}
	}
}

// GetStatistics returns pool statistics
func (cp *ConnectionPool) GetStatistics() map[string]interface{} {
	cp.mu.RLock()
	defer cp.mu.RUnlock()

	stats := map[string]interface{}{
		"total_connections": len(cp.connections),
		"healthy":           0,
		"unhealthy":         0,
		"circuit_open":      0,
	}

	for _, pooled := range cp.connections {
		pooled.mu.RLock()
		if pooled.isHealthy {
			stats["healthy"] = stats["healthy"].(int) + 1
		} else {
			stats["unhealthy"] = stats["unhealthy"].(int) + 1
		}
		if pooled.circuitState == CircuitOpen {
			stats["circuit_open"] = stats["circuit_open"].(int) + 1
		}
		pooled.mu.RUnlock()
	}

	return stats
}

// Close closes all connections and stops maintenance
func (cp *ConnectionPool) Close() error {
	close(cp.stopCleanup)

	cp.mu.Lock()
	defer cp.mu.Unlock()

	for _, pooled := range cp.connections {
		if pooled.conn != nil {
			pooled.conn.Close()
		}
	}

	cp.connections = make(map[string]*PooledConnection)
	return nil
}

// Helper methods for PooledConnection

func (pc *PooledConnection) isUsable() bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	// Check circuit breaker
	if pc.circuitState == CircuitOpen {
		return false
	}

	// Check connection state if conn exists
	if pc.conn != nil {
		state := pc.conn.GetState()
		if state == connectivity.Shutdown || state == connectivity.TransientFailure {
			return false
		}
	}

	return pc.isHealthy
}

func (pc *PooledConnection) recordUse() {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	pc.lastUsed = time.Now()
	pc.useCount++

	// Success in half-open state moves to closed
	if pc.circuitState == CircuitHalfOpen {
		pc.circuitState = CircuitClosed
		pc.failures = 0
	}
}
