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

	"google.golang.org/grpc"
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


// SecureDialWithCerts creates a secure gRPC connection using individual certificate paths
func SecureDialWithCerts(ctx context.Context, target, caPath, certPath, keyPath string) (*grpc.ClientConn, error) {
	opts := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                10 * time.Second,
			Timeout:             3 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	// Create TLS configuration from individual certificate files
	tlsConfig, err := createTLSConfigFromFiles(caPath, certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS config from files: %w", err)
	}
	
	opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))

	conn, err := grpc.DialContext(ctx, target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", target, err)
	}

	return conn, nil
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


// createTLSConfigFromFiles creates a TLS configuration from individual certificate file paths
func createTLSConfigFromFiles(caPath, certPath, keyPath string) (*tls.Config, error) {
	// Load client certificate
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	// Create TLS config
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	// Load CA certificate for server verification
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