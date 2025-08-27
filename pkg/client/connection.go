package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"time"

	"collective/pkg/auth"
	"collective/pkg/config"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// ConnectionPool manages secure gRPC connections with connection pooling
type ConnectionPool struct {
	connections map[string]*grpc.ClientConn
	mutex       sync.RWMutex
	authConfig  *auth.AuthConfig
}

// GlobalPool is the shared connection pool for the application
var GlobalPool = &ConnectionPool{
	connections: make(map[string]*grpc.ClientConn),
}

// SecureDialWithContext creates a secure gRPC connection using the current auth context
func SecureDialWithContext(ctx context.Context, target string, configCtx *config.Context) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             3 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	// If we have a valid auth context with certificates, use TLS
	if configCtx != nil && configCtx.Auth.CertPath != "" && configCtx.Auth.KeyPath != "" && !configCtx.Auth.Insecure {
		tlsConfig, err := createTLSConfig(configCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to create TLS config: %w", err)
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		// Fallback to insecure for backward compatibility
		// TODO: Make this configurable or remove once all clients are updated
		opts = append(opts, grpc.WithInsecure())
	}

	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", target, err)
	}

	return conn, nil
}

// SecureDial creates a secure connection without specific context (uses default)
func SecureDial(target string) (*grpc.ClientConn, error) {
	return SecureDialWithContext(context.Background(), target, nil)
}

// GetPooledConnection returns a connection from the pool or creates a new one
func (p *ConnectionPool) GetPooledConnection(ctx context.Context, target string, configCtx *config.Context) (*grpc.ClientConn, error) {
	p.mutex.RLock()
	conn, exists := p.connections[target]
	p.mutex.RUnlock()

	if exists && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	// Create new connection
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Double-check after acquiring write lock
	conn, exists = p.connections[target]
	if exists && conn.GetState() != connectivity.Shutdown {
		return conn, nil
	}

	// Create new secure connection
	newConn, err := SecureDialWithContext(ctx, target, configCtx)
	if err != nil {
		return nil, err
	}

	// Store in pool
	p.connections[target] = newConn
	return newConn, nil
}

// CloseAll closes all connections in the pool
func (p *ConnectionPool) CloseAll() {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, conn := range p.connections {
		conn.Close()
	}
	p.connections = make(map[string]*grpc.ClientConn)
}

// createTLSConfig creates a TLS configuration from the context
func createTLSConfig(configCtx *config.Context) (*tls.Config, error) {
	// Load client certificate
	cert, err := tls.LoadX509KeyPair(configCtx.Auth.CertPath, configCtx.Auth.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	// Create TLS config
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// If we have a CA cert, use it for server verification
	if configCtx.Auth.CAPath != "" {
		// Load CA certificate manually
		caCertPEM, err := os.ReadFile(configCtx.Auth.CAPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCertPEM) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
}

// CreateAuthenticatedConnection creates a connection with full mTLS authentication
func CreateAuthenticatedConnection(ctx context.Context, target string, authConfig *auth.AuthConfig) (*grpc.ClientConn, error) {
	if authConfig == nil {
		return nil, fmt.Errorf("auth config is required for authenticated connection")
	}

	if !authConfig.Enabled || authConfig.CertPath == "" || authConfig.KeyPath == "" {
		// Fallback to insecure if auth not properly configured
		return grpc.DialContext(ctx, target,
			grpc.WithInsecure(),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                10 * time.Second,
				Timeout:             3 * time.Second,
				PermitWithoutStream: true,
			}),
		)
	}

	// Load client certificate
	cert, err := tls.LoadX509KeyPair(authConfig.CertPath, authConfig.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	// Create TLS config
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// If we have a CA cert, use it for server verification
	caPath := authConfig.CAPath
	if caPath == "" {
		caPath = authConfig.ClientCAPath
	}
	if caPath != "" {
		caCertPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCertPEM) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsConfig.RootCAs = caCertPool
	}

	creds := credentials.NewTLS(tlsConfig)

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             3 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create authenticated connection to %s: %w", target, err)
	}

	return conn, nil
}