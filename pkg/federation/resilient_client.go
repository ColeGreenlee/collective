package federation

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

// ResilientClient provides resilient RPC calls with retry and failover
type ResilientClient struct {
	pool           *ConnectionPool
	gossip         *GossipService
	logger         *zap.Logger
	
	// Retry configuration
	maxRetries     int
	baseDelay      time.Duration
	maxDelay       time.Duration
	jitterFactor   float64
	
	// Failover configuration
	failoverEnabled bool
	preferredNodes  map[string][]string // domain -> preferred endpoints
	mu              sync.RWMutex
}

// RetryableFunc is a function that can be retried
type RetryableFunc func(ctx context.Context, conn *grpc.ClientConn) error

// NewResilientClient creates a new resilient client
func NewResilientClient(pool *ConnectionPool, gossip *GossipService, logger *zap.Logger) *ResilientClient {
	if logger == nil {
		logger = zap.NewNop()
	}
	
	return &ResilientClient{
		pool:            pool,
		gossip:          gossip,
		logger:          logger,
		maxRetries:      3,
		baseDelay:       100 * time.Millisecond,
		maxDelay:        5 * time.Second,
		jitterFactor:    0.2,
		failoverEnabled: true,
		preferredNodes:  make(map[string][]string),
	}
}

// CallWithRetry executes an RPC call with automatic retry and failover
func (rc *ResilientClient) CallWithRetry(ctx context.Context, domain string, operation string, fn RetryableFunc) error {
	// Get available endpoints from gossip
	endpoints := rc.getEndpointsForDomain(domain)
	if len(endpoints) == 0 {
		return fmt.Errorf("no endpoints available for domain %s", domain)
	}
	
	var lastErr error
	attemptCount := 0
	
	// Try each endpoint with retries
	for _, endpoint := range endpoints {
		// Get or create connection
		conn, err := rc.pool.GetConnection(domain, []string{endpoint})
		if err != nil {
			rc.logger.Debug("Failed to get connection",
				zap.String("domain", domain),
				zap.String("endpoint", endpoint),
				zap.Error(err))
			lastErr = err
			continue
		}
		
		// Try the operation with retries on this endpoint
		err = rc.retryOperation(ctx, conn, domain, operation, fn, attemptCount)
		if err == nil {
			// Success!
			rc.pool.ReleaseConnection(domain, conn)
			return nil
		}
		
		// Check if error is retryable
		if !rc.isRetryableError(err) {
			rc.pool.ReleaseConnection(domain, conn)
			return err
		}
		
		lastErr = err
		attemptCount++
		
		// Mark endpoint as unhealthy if too many failures
		if attemptCount >= rc.maxRetries {
			rc.pool.MarkUnhealthy(domain)
			
			// Try failover if enabled
			if rc.failoverEnabled {
				rc.logger.Info("Attempting failover",
					zap.String("domain", domain),
					zap.String("failed_endpoint", endpoint))
			}
		}
	}
	
	return fmt.Errorf("all attempts failed for %s: %w", operation, lastErr)
}

// retryOperation performs the operation with exponential backoff retry
func (rc *ResilientClient) retryOperation(
	ctx context.Context,
	conn *grpc.ClientConn,
	domain string,
	operation string,
	fn RetryableFunc,
	baseAttempt int,
) error {
	var lastErr error
	
	for attempt := 0; attempt < rc.maxRetries; attempt++ {
		// Check context cancellation
		if ctx.Err() != nil {
			return ctx.Err()
		}
		
		// Calculate total attempt number
		totalAttempt := baseAttempt + attempt
		
		// Execute the operation
		err := fn(ctx, conn)
		if err == nil {
			return nil // Success!
		}
		
		// Check if error is retryable
		if !rc.isRetryableError(err) {
			return err
		}
		
		lastErr = err
		
		// Log retry attempt
		rc.logger.Debug("Operation failed, retrying",
			zap.String("domain", domain),
			zap.String("operation", operation),
			zap.Int("attempt", totalAttempt+1),
			zap.Error(err))
		
		// Don't sleep on the last attempt
		if attempt < rc.maxRetries-1 {
			delay := rc.calculateBackoff(totalAttempt)
			
			// Sleep with context cancellation
			select {
			case <-time.After(delay):
				// Continue to next attempt
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	
	return lastErr
}

// calculateBackoff calculates the exponential backoff delay with jitter
func (rc *ResilientClient) calculateBackoff(attempt int) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	delay := float64(rc.baseDelay) * math.Pow(2, float64(attempt))
	
	// Cap at maxDelay
	if delay > float64(rc.maxDelay) {
		delay = float64(rc.maxDelay)
	}
	
	// Add jitter (±jitterFactor)
	jitter := delay * rc.jitterFactor * (2*rand.Float64() - 1)
	delay += jitter
	
	// Ensure delay is positive
	if delay < 0 {
		delay = float64(rc.baseDelay)
	}
	
	return time.Duration(delay)
}

// isRetryableError determines if an error should trigger a retry
func (rc *ResilientClient) isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	
	// Check for gRPC status codes
	st, ok := status.FromError(err)
	if !ok {
		// Not a gRPC error, consider it retryable
		return true
	}
	
	// Retryable gRPC codes
	switch st.Code() {
	case codes.Unavailable,
		codes.ResourceExhausted,
		codes.Aborted,
		codes.DeadlineExceeded,
		codes.Internal:
		return true
	case codes.Unknown:
		// Sometimes network errors come as Unknown
		return true
	default:
		// Non-retryable errors (InvalidArgument, NotFound, PermissionDenied, etc.)
		return false
	}
}

// getEndpointsForDomain gets available endpoints for a domain
func (rc *ResilientClient) getEndpointsForDomain(domain string) []string {
	// Check preferred endpoints first
	rc.mu.RLock()
	preferred, hasPreferred := rc.preferredNodes[domain]
	rc.mu.RUnlock()
	
	if hasPreferred && len(preferred) > 0 {
		return preferred
	}
	
	// Get from gossip service
	if rc.gossip != nil {
		peers := rc.gossip.GetHealthyPeers()
		for _, peer := range peers {
			if peer.Address.Domain == domain {
				endpoints := []string{}
				for _, ep := range peer.Endpoints {
					endpoints = append(endpoints, ep.DirectIPs...)
				}
				
				// Cache for future use
				rc.mu.Lock()
				rc.preferredNodes[domain] = endpoints
				rc.mu.Unlock()
				
				return endpoints
			}
		}
	}
	
	// Fallback: construct default endpoint
	return []string{domain + ":8001"}
}

// SetPreferredEndpoints sets preferred endpoints for a domain
func (rc *ResilientClient) SetPreferredEndpoints(domain string, endpoints []string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	
	rc.preferredNodes[domain] = endpoints
}

// ClearPreferredEndpoints clears preferred endpoints for a domain
func (rc *ResilientClient) ClearPreferredEndpoints(domain string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	
	delete(rc.preferredNodes, domain)
}

// ConfigureRetry sets retry parameters
func (rc *ResilientClient) ConfigureRetry(maxRetries int, baseDelay, maxDelay time.Duration) {
	rc.maxRetries = maxRetries
	rc.baseDelay = baseDelay
	rc.maxDelay = maxDelay
}

// EnableFailover enables or disables automatic failover
func (rc *ResilientClient) EnableFailover(enabled bool) {
	rc.failoverEnabled = enabled
}

// GetStatistics returns client statistics
func (rc *ResilientClient) GetStatistics() map[string]interface{} {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	
	stats := map[string]interface{}{
		"max_retries":       rc.maxRetries,
		"failover_enabled":  rc.failoverEnabled,
		"preferred_domains": len(rc.preferredNodes),
	}
	
	// Add pool statistics
	if rc.pool != nil {
		poolStats := rc.pool.GetStatistics()
		for k, v := range poolStats {
			stats["pool_"+k] = v
		}
	}
	
	return stats
}

// HealthCheck performs a health check on a domain
func (rc *ResilientClient) HealthCheck(ctx context.Context, domain string) error {
	// Simple ping operation
	return rc.CallWithRetry(ctx, domain, "health_check", func(ctx context.Context, conn *grpc.ClientConn) error {
		// In a real implementation, this would call a health check RPC
		// For now, we just check connection state
		state := conn.GetState()
		if state == connectivity.Ready || state == connectivity.Idle {
			return nil
		}
		return fmt.Errorf("connection not ready: %v", state)
	})
}