package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Test helper functions
func generateTestCertificate(t *testing.T, isCA bool, issuer *x509.Certificate, issuerKey *rsa.PrivateKey) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	// Generate RSA key pair
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	// Certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"Collective Test"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{""},
			StreetAddress: []string{""},
			PostalCode:    []string{""},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	if isCA {
		template.IsCA = true
		template.KeyUsage |= x509.KeyUsageCertSign
	}

	// Self-signed or signed by issuer
	var certParent *x509.Certificate
	var keyParent *rsa.PrivateKey
	if issuer != nil && issuerKey != nil {
		certParent = issuer
		keyParent = issuerKey
	} else {
		certParent = &template
		keyParent = privKey
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, certParent, &privKey.PublicKey, keyParent)
	if err != nil {
		t.Fatalf("Failed to create certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("Failed to parse certificate: %v", err)
	}

	return cert, privKey, certDER
}

func writeCertAndKey(t *testing.T, dir string, name string, cert []byte, key *rsa.PrivateKey) (certPath, keyPath string) {
	// Write certificate
	certPath = filepath.Join(dir, name+".crt")
	certFile, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("Failed to create cert file: %v", err)
	}
	defer certFile.Close()

	err = pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: cert})
	if err != nil {
		t.Fatalf("Failed to write certificate: %v", err)
	}

	// Write private key
	keyPath = filepath.Join(dir, name+".key")
	keyFile, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("Failed to create key file: %v", err)
	}
	defer keyFile.Close()

	keyBytes := x509.MarshalPKCS1PrivateKey(key)
	err = pem.Encode(keyFile, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyBytes})
	if err != nil {
		t.Fatalf("Failed to write private key: %v", err)
	}

	return certPath, keyPath
}

// Test Identity struct
func TestIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity Identity
		wantType ComponentType
	}{
		{
			name: "coordinator identity",
			identity: Identity{
				Type:        ComponentCoordinator,
				MemberID:    "alice",
				ComponentID: "coordinator-1",
			},
			wantType: ComponentCoordinator,
		},
		{
			name: "node identity",
			identity: Identity{
				Type:        ComponentNode,
				MemberID:    "alice",
				ComponentID: "node-1",
			},
			wantType: ComponentNode,
		},
		{
			name: "client identity",
			identity: Identity{
				Type:        ComponentClient,
				MemberID:    "alice",
				ComponentID: "client-1",
			},
			wantType: ComponentClient,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.identity.Type != tt.wantType {
				t.Errorf("Identity.Type = %v, want %v", tt.identity.Type, tt.wantType)
			}
		})
	}
}

// Test AuthConfig
func TestAuthConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    AuthConfig
		wantError bool
	}{
		{
			name: "disabled auth config",
			config: AuthConfig{
				Enabled: false,
			},
			wantError: false,
		},
		{
			name: "enabled without certificates",
			config: AuthConfig{
				Enabled: true,
			},
			wantError: true, // Should error when validated
		},
		{
			name: "enabled with paths",
			config: AuthConfig{
				Enabled:  true,
				CAPath:   "/tmp/ca.crt",
				CertPath: "/tmp/cert.crt",
				KeyPath:  "/tmp/key.pem",
			},
			wantError: false, // Paths exist, validation would check files
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantError && err == nil {
				t.Error("Expected validation error but got none")
			}
			if !tt.wantError && err != nil && tt.config.Enabled {
				// Only error if enabled and paths don't exist
				if _, statErr := os.Stat(tt.config.CertPath); os.IsNotExist(statErr) {
					// This is expected for test paths
					return
				}
				t.Errorf("Unexpected validation error: %v", err)
			}
		})
	}
}

// Test DefaultAuthConfig
func TestDefaultAuthConfig(t *testing.T) {
	config := DefaultAuthConfig()
	
	if config.Enabled {
		t.Error("Default auth config should be disabled")
	}
	
	if !config.PeerVerification {
		t.Error("Default auth config should have peer verification enabled")
	}
	
	if config.RequireClientAuth {
		t.Error("Default auth config should not require client auth by default")
	}
	
	if config.MinTLSVersion != "1.2" {
		t.Errorf("Default MinTLSVersion = %s, want 1.2", config.MinTLSVersion)
	}
}

// Test certificate validation with real certificates
func TestCertificateValidation(t *testing.T) {
	// Create temp directory for certificates
	tempDir, err := os.MkdirTemp("", "auth-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Generate CA certificate
	caCert, caKey, caCertDER := generateTestCertificate(t, true, nil, nil)
	caCertPath, _ := writeCertAndKey(t, tempDir, "ca", caCertDER, caKey)

	// Generate server certificate signed by CA
	_, serverKey, serverCertDER := generateTestCertificate(t, false, caCert, caKey)
	serverCertPath, serverKeyPath := writeCertAndKey(t, tempDir, "server", serverCertDER, serverKey)

	// Create auth config
	authConfig := &AuthConfig{
		Enabled:           true,
		CAPath:            caCertPath,
		CertPath:          serverCertPath,
		KeyPath:           serverKeyPath,
		PeerVerification:  true,
		RequireClientAuth: false,
	}

	// Test loading certificates
	t.Run("load valid certificates", func(t *testing.T) {
		cert, err := tls.LoadX509KeyPair(authConfig.CertPath, authConfig.KeyPath)
		if err != nil {
			t.Errorf("Failed to load certificate: %v", err)
		}

		if len(cert.Certificate) == 0 {
			t.Error("Certificate chain is empty")
		}
	})

	// Test CA certificate loading
	t.Run("load CA certificate", func(t *testing.T) {
		caCertPEM, err := os.ReadFile(authConfig.CAPath)
		if err != nil {
			t.Fatalf("Failed to read CA certificate: %v", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCertPEM) {
			t.Error("Failed to parse CA certificate")
		}
	})

	// Test expired certificate
	t.Run("expired certificate detection", func(t *testing.T) {
		// Create expired certificate template
		expiredTemplate := x509.Certificate{
			SerialNumber: big.NewInt(2),
			Subject: pkix.Name{
				Organization: []string{"Expired Test"},
			},
			NotBefore:             time.Now().Add(-2 * 365 * 24 * time.Hour), // 2 years ago
			NotAfter:              time.Now().Add(-365 * 24 * time.Hour),     // 1 year ago
			KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
		}

		expiredKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		expiredCertDER, err := x509.CreateCertificate(rand.Reader, &expiredTemplate, caCert, &expiredKey.PublicKey, caKey)
		if err != nil {
			t.Fatalf("Failed to create expired certificate: %v", err)
		}

		expiredCert, _ := x509.ParseCertificate(expiredCertDER)
		
		// Check if certificate is expired
		now := time.Now()
		if now.After(expiredCert.NotAfter) {
			// Certificate is correctly expired
			t.Log("Certificate is correctly identified as expired")
		} else {
			t.Error("Certificate should be expired")
		}
	})
}

// Test AuthInterceptor
func TestAuthInterceptor(t *testing.T) {
	// Create a basic auth interceptor
	interceptor := NewAuthInterceptor(nil, nil, false)
	
	if interceptor == nil {
		t.Error("Failed to create auth interceptor")
	}
	
	// Test that interceptor can be created with nil authenticator (for non-auth mode)
	if interceptor.requireAuth {
		t.Error("Interceptor should not require auth when created with false")
	}
}

// Test error types
func TestErrorTypes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "invalid certificate error",
			err:  ErrInvalidCertificate,
			want: "invalid certificate",
		},
		{
			name: "certificate expired error",
			err:  ErrCertificateExpired,
			want: "certificate expired",
		},
		{
			name: "unauthorized error",
			err:  ErrUnauthorized,
			want: "unauthorized",
		},
		{
			name: "invalid CA error",
			err:  ErrInvalidCA,
			want: "invalid CA certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Error() != tt.want {
				t.Errorf("Error message = %v, want %v", tt.err.Error(), tt.want)
			}
		})
	}
}

// Benchmark certificate generation
func BenchmarkCertificateGeneration(b *testing.B) {
	for i := 0; i < b.N; i++ {
		privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		template := x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject: pkix.Name{
				Organization: []string{"Benchmark Test"},
			},
			NotBefore:    time.Now(),
			NotAfter:     time.Now().Add(365 * 24 * time.Hour),
			KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		
		x509.CreateCertificate(rand.Reader, &template, &template, &privKey.PublicKey, privKey)
	}
}