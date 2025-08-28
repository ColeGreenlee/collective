package shared

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"collective/pkg/auth"
	"collective/pkg/client"
	"collective/pkg/config"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// DefaultGRPCTimeout is the default timeout for gRPC operations
	DefaultGRPCTimeout = 30 * time.Second
	
	// StreamingThreshold is the size above which we use streaming
	StreamingThreshold = 4 * 1024 * 1024 // 4MB (just under gRPC limit)
)

// ConnectionManager provides standardized connection management across all components
type ConnectionManager struct {
	authConfig *auth.AuthConfig
}

// NewConnectionManager creates a new connection manager
func NewConnectionManager(authConfig *auth.AuthConfig) *ConnectionManager {
	return &ConnectionManager{
		authConfig: authConfig,
	}
}

// ConnectToCoordinator creates a secure connection to a coordinator
func ConnectToCoordinator(coordinatorAddr string) (*grpc.ClientConn, error) {
	return ConnectToCoordinatorWithTimeout(coordinatorAddr, DefaultGRPCTimeout)
}

// ConnectToCoordinatorWithTimeout creates a secure connection to a coordinator with timeout
func ConnectToCoordinatorWithTimeout(coordinatorAddr string, timeout time.Duration) (*grpc.ClientConn, error) {
	return ConnectToCoordinatorWithAuth(coordinatorAddr, timeout, nil)
}

// ConnectToCoordinatorWithAuth creates a secure connection to a coordinator with auth config
func ConnectToCoordinatorWithAuth(coordinatorAddr string, timeout time.Duration, authConfig *auth.AuthConfig) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	// If auth config is provided, use it directly
	if authConfig != nil && authConfig.Enabled {
		tlsBuilder, err := auth.NewTLSConfigBuilder(authConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS config builder: %w", err)
		}

		tlsConfig, err := tlsBuilder.BuildClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build TLS config: %w", err)
		}

		if tlsConfig != nil {
			// Extract hostname from address for SNI
			host := coordinatorAddr
			if h, _, err := net.SplitHostPort(coordinatorAddr); err == nil {
				host = h
			}
			tlsConfig.ServerName = host
			
			return grpc.DialContext(ctx, coordinatorAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
		}
	}
	
	// Try to load unified configuration for auth
	clientConfig, err := config.LoadClientConfig()
	if err == nil && len(clientConfig.Collectives) > 0 {
		connectionConfig, err := clientConfig.ResolveConnection(coordinatorAddr)
		if err == nil {
			// Update coordinatorAddr to the resolved address
			if coordinatorAddr == "" {
				coordinatorAddr = connectionConfig.Coordinator
			}
			// Use federated connection configuration
			if connectionConfig.CAPath != "" && connectionConfig.CertPath != "" && connectionConfig.KeyPath != "" {
				return client.SecureDialWithCerts(ctx, connectionConfig.Coordinator, 
					connectionConfig.CAPath, connectionConfig.CertPath, connectionConfig.KeyPath)
			}
		}
	}
	
	// Fallback to insecure connection if no federated config is available
	return grpc.DialContext(ctx, coordinatorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// ConnectToNode creates a secure connection to a storage node using auth config
func (cm *ConnectionManager) ConnectToNode(address string) (*grpc.ClientConn, error) {
	return cm.ConnectToNodeWithTimeout(address, DefaultGRPCTimeout)
}

// ConnectToNodeWithTimeout creates a secure connection to a storage node with timeout
func (cm *ConnectionManager) ConnectToNodeWithTimeout(address string, timeout time.Duration) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	
	var dialOpts []grpc.DialOption
	
	// Use TLS if configured
	if cm.authConfig != nil && cm.authConfig.Enabled {
		tlsBuilder, err := auth.NewTLSConfigBuilder(cm.authConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS config builder: %w", err)
		}

		tlsConfig, err := tlsBuilder.BuildClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to build TLS config: %w", err)
		}

		if tlsConfig != nil {
			// Extract hostname from address for SNI
			host := address
			if h, _, err := net.SplitHostPort(address); err == nil {
				host = h
			}
			tlsConfig.ServerName = host
			
			dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
		}
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	return grpc.DialContext(ctx, address, dialOpts...)
}

// ConnectWithRetry creates a connection with retry logic
func (cm *ConnectionManager) ConnectWithRetry(address string, maxRetries int, retryInterval time.Duration) (*grpc.ClientConn, error) {
	var lastErr error
	
	for attempt := 0; attempt < maxRetries; attempt++ {
		conn, err := cm.ConnectToNode(address)
		if err == nil {
			return conn, nil
		}
		
		lastErr = err
		if attempt < maxRetries-1 {
			time.Sleep(retryInterval)
		}
	}
	
	return nil, fmt.Errorf("failed to connect after %d attempts: %w", maxRetries, lastErr)
}

// ShouldUseStreaming determines if a data size requires streaming
func ShouldUseStreaming(dataSize int64) bool {
	return dataSize > StreamingThreshold
}

// SharedConnectionPool provides pooled connections with health monitoring
type SharedConnectionPool struct {
	connections map[string]*grpc.ClientConn
	mutex       sync.RWMutex
	authConfig  *auth.AuthConfig
}

// NewSharedConnectionPool creates a new shared connection pool
func NewSharedConnectionPool(authConfig *auth.AuthConfig) *SharedConnectionPool {
	return &SharedConnectionPool{
		connections: make(map[string]*grpc.ClientConn),
		authConfig:  authConfig,
	}
}

// GetConnection returns a pooled connection or creates a new one
func (p *SharedConnectionPool) GetConnection(address string) (*grpc.ClientConn, error) {
	p.mutex.RLock()
	conn, exists := p.connections[address]
	p.mutex.RUnlock()

	if exists && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	// Create new connection
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Double-check after acquiring write lock
	conn, exists = p.connections[address]
	if exists && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	// Create new secure connection
	cm := NewConnectionManager(p.authConfig)
	newConn, err := cm.ConnectToNode(address)
	if err != nil {
		return nil, err
	}

	// Store in pool
	p.connections[address] = newConn
	return newConn, nil
}

// CloseAll closes all connections in the pool
func (p *SharedConnectionPool) CloseAll() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, conn := range p.connections {
		conn.Close()
	}
	p.connections = make(map[string]*grpc.ClientConn)
}