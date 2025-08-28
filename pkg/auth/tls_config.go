package auth

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TLSConfigBuilder builds TLS configurations for different components
type TLSConfigBuilder struct {
	config      *AuthConfig
	certManager *CertManager
}

// NewTLSConfigBuilder creates a new TLS configuration builder
func NewTLSConfigBuilder(config *AuthConfig) (*TLSConfigBuilder, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	var certManager *CertManager
	if config.Enabled && config.CAPath != "" {
		cm, err := NewCertManager(config.CAPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create certificate manager: %w", err)
		}
		certManager = cm
	}

	return &TLSConfigBuilder{
		config:      config,
		certManager: certManager,
	}, nil
}

// BuildServerConfig creates TLS configuration for servers (coordinators/nodes)
func (b *TLSConfigBuilder) BuildServerConfig() (*tls.Config, error) {
	if !b.config.Enabled {
		return nil, nil
	}

	// Load server certificate and key
	cert, err := tls.LoadX509KeyPair(b.config.CertPath, b.config.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   b.getTLSVersion(),
		CipherSuites: b.getCipherSuites(),
	}

	// Setup client authentication if required
	if b.config.RequireClientAuth {
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert

		// Load client CA pool
		clientCAPool, err := b.loadCAPool(b.config.ClientCAPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client CA pool: %w", err)
		}
		tlsConfig.ClientCAs = clientCAPool

		// Add custom verification
		tlsConfig.VerifyPeerCertificate = b.verifyPeerCertificate
	}

	return tlsConfig, nil
}

// BuildClientConfig creates TLS configuration for clients
func (b *TLSConfigBuilder) BuildClientConfig() (*tls.Config, error) {
	if !b.config.Enabled {
		return &tls.Config{InsecureSkipVerify: true}, nil
	}

	tlsConfig := &tls.Config{
		MinVersion:   b.getTLSVersion(),
		CipherSuites: b.getCipherSuites(),
	}

	// Load CA pool for server verification
	caPool, err := b.loadCAPool(b.config.CAPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load CA pool: %w", err)
	}
	tlsConfig.RootCAs = caPool

	// Load client certificate if provided
	if b.config.CertPath != "" && b.config.KeyPath != "" {
		cert, err := tls.LoadX509KeyPair(b.config.CertPath, b.config.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	// Add peer verification if enabled
	if b.config.PeerVerification {
		tlsConfig.VerifyPeerCertificate = b.verifyPeerCertificate
	}

	return tlsConfig, nil
}

// BuildPeerConfig creates TLS configuration for peer connections (coordinator-to-coordinator)
func (b *TLSConfigBuilder) BuildPeerConfig() (*tls.Config, error) {
	if !b.config.Enabled {
		return &tls.Config{InsecureSkipVerify: true}, nil
	}

	// Load server certificate and key
	cert, err := tls.LoadX509KeyPair(b.config.CertPath, b.config.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load peer certificate: %w", err)
	}

	// Load CA pool with all member CAs for federation trust
	caPool, err := b.LoadMultiCAPool(filepath.Dir(b.config.CAPath))
	if err != nil {
		return nil, fmt.Errorf("failed to load federation CA pool: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   b.getTLSVersion(),
		CipherSuites: b.getCipherSuites(),
	}

	// Always verify peers
	tlsConfig.VerifyPeerCertificate = b.verifyPeerCertificate

	return tlsConfig, nil
}

// verifyPeerCertificate performs custom certificate verification
func (b *TLSConfigBuilder) verifyPeerCertificate(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("no certificates provided")
	}

	// Parse the peer certificate
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("failed to parse peer certificate: %w", err)
	}

	// Extract identity from certificate
	if b.certManager != nil {
		identity, err := b.certManager.GetIdentityFromCert(cert)
		if err != nil {
			return fmt.Errorf("failed to extract identity: %w", err)
		}

		// Verify member ID if restrictions are configured
		if len(b.config.AllowedMemberIDs) > 0 {
			allowed := false
			for _, memberID := range b.config.AllowedMemberIDs {
				if identity.MemberID == memberID {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("member ID %s not allowed", identity.MemberID)
			}
		}
	}

	return nil
}

// loadCAPool loads a CA certificate pool from file
func (b *TLSConfigBuilder) loadCAPool(path string) (*x509.CertPool, error) {
	if path == "" {
		path = b.config.CAPath
	}

	caCert, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate")
	}

	return caPool, nil
}

// LoadMultiCAPool loads multiple CA certificates from a directory for peer trust
func (b *TLSConfigBuilder) LoadMultiCAPool(certDir string) (*x509.CertPool, error) {
	caPool := x509.NewCertPool()

	// Try to load all CA certificates from the directory
	entries, err := os.ReadDir(certDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cert directory: %w", err)
	}

	loadedCount := 0
	for _, entry := range entries {
		// Load both member CAs (*-ca.crt) and federation root CA (federation-root-ca.crt)
		if strings.HasSuffix(entry.Name(), "-ca.crt") || entry.Name() == "federation-root-ca.crt" {
			caPath := filepath.Join(certDir, entry.Name())
			caCert, err := os.ReadFile(caPath)
			if err != nil {
				// Log but continue with other CAs
				continue
			}
			if caPool.AppendCertsFromPEM(caCert) {
				loadedCount++
			}
		}
	}

	if loadedCount == 0 {
		return nil, fmt.Errorf("no CA certificates found in directory")
	}

	return caPool, nil
}

// getTLSVersion returns the minimum TLS version from config
func (b *TLSConfigBuilder) getTLSVersion() uint16 {
	switch b.config.MinTLSVersion {
	case "1.3":
		return tls.VersionTLS13
	case "1.2":
		return tls.VersionTLS12
	default:
		return tls.VersionTLS12
	}
}

// getCipherSuites returns secure cipher suites optimized for Ed25519
func (b *TLSConfigBuilder) getCipherSuites() []uint16 {
	// TLS 1.3 cipher suites (automatically used with TLS 1.3)
	// These work well with Ed25519

	// For TLS 1.2, use ECDHE cipher suites which are compatible with Ed25519 certs
	return []uint16{
		// TLS 1.2 ECDHE cipher suites
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
	}
}

// LoadKeyPair loads an Ed25519 certificate and key pair
func LoadEd25519KeyPair(certPath, keyPath string) (tls.Certificate, error) {
	// Load certificate
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to read certificate: %w", err)
	}

	// Load private key
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to read private key: %w", err)
	}

	// Parse the certificate and key
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to parse certificate and key: %w", err)
	}

	// Verify it's an Ed25519 key
	if _, ok := cert.PrivateKey.(ed25519.PrivateKey); !ok {
		return tls.Certificate{}, fmt.Errorf("private key is not Ed25519")
	}

	return cert, nil
}
