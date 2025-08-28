package federation

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TrustStore manages multiple CA certificates for federation trust validation
type TrustStore struct {
	mu sync.RWMutex

	// Root CA for the federation
	rootCA *x509.Certificate

	// Member CAs (intermediate certificates)
	memberCAs map[string]*x509.Certificate // domain -> CA cert

	// Certificate pools for validation
	rootPool   *x509.CertPool
	memberPool *x509.CertPool

	// Validation cache for performance
	validationCache map[string]*validationCacheEntry
	cacheTTL        time.Duration
}

// validationCacheEntry holds cached validation results
type validationCacheEntry struct {
	valid     bool
	expiresAt time.Time
	memberID  string // which member's CA validated this cert
}

// NewTrustStore creates a new trust store
func NewTrustStore() *TrustStore {
	return &TrustStore{
		memberCAs:       make(map[string]*x509.Certificate),
		validationCache: make(map[string]*validationCacheEntry),
		cacheTTL:        5 * time.Minute,
		rootPool:        x509.NewCertPool(),
		memberPool:      x509.NewCertPool(),
	}
}

// LoadFederationRootCA loads the federation root CA
func (ts *TrustStore) LoadFederationRootCA(certPath string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	certPEM, err := ioutil.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read root CA certificate: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("failed to parse root CA PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse root CA certificate: %w", err)
	}

	if !cert.IsCA {
		return fmt.Errorf("certificate is not a CA certificate")
	}

	ts.rootCA = cert
	ts.rootPool = x509.NewCertPool()
	ts.rootPool.AddCert(cert)

	return nil
}

// AddMemberCA adds a member's intermediate CA to the trust store
func (ts *TrustStore) AddMemberCA(memberDomain string, certPath string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	certPEM, err := ioutil.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read member CA certificate: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("failed to parse member CA PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse member CA certificate: %w", err)
	}

	if !cert.IsCA {
		return fmt.Errorf("certificate is not a CA certificate")
	}

	// Verify the member CA is signed by the root CA
	if ts.rootCA != nil {
		opts := x509.VerifyOptions{
			Roots: ts.rootPool,
			KeyUsages: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
				x509.ExtKeyUsageClientAuth,
			},
		}

		if _, err := cert.Verify(opts); err != nil {
			return fmt.Errorf("member CA not signed by federation root CA: %w", err)
		}
	}

	ts.memberCAs[memberDomain] = cert
	ts.memberPool.AddCert(cert)

	// Clear validation cache when CA is added
	ts.clearCacheLocked()

	return nil
}

// RemoveMemberCA removes a member CA from the trust store
func (ts *TrustStore) RemoveMemberCA(memberDomain string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if _, exists := ts.memberCAs[memberDomain]; !exists {
		return fmt.Errorf("member CA for %s not found", memberDomain)
	}

	delete(ts.memberCAs, memberDomain)

	// Rebuild member pool without the removed CA
	ts.memberPool = x509.NewCertPool()
	for _, cert := range ts.memberCAs {
		ts.memberPool.AddCert(cert)
	}

	// Clear validation cache when CA is removed
	ts.clearCacheLocked()

	return nil
}

// LoadCADirectory loads all CA certificates from a directory
func (ts *TrustStore) LoadCADirectory(dirPath string) error {
	// Look for root CA
	rootCAPath := filepath.Join(dirPath, "ca.crt")
	if _, err := os.Stat(rootCAPath); err == nil {
		if err := ts.LoadFederationRootCA(rootCAPath); err != nil {
			return fmt.Errorf("failed to load root CA: %w", err)
		}
	}

	// Load member CAs
	files, err := ioutil.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read CA directory: %w", err)
	}

	for _, file := range files {
		if filepath.Ext(file.Name()) == ".crt" && file.Name() != "ca.crt" {
			// Extract member domain from filename (e.g., alice.collective.local-ca.crt)
			name := file.Name()
			if len(name) > 7 && name[len(name)-7:] == "-ca.crt" {
				memberDomain := name[:len(name)-7]
				certPath := filepath.Join(dirPath, file.Name())

				if err := ts.AddMemberCA(memberDomain, certPath); err != nil {
					// Log error but continue loading other CAs
					fmt.Fprintf(os.Stderr, "Warning: failed to load member CA %s: %v\n", memberDomain, err)
				}
			}
		}
	}

	return nil
}

// ValidateCertificate validates a certificate against the trust store
func (ts *TrustStore) ValidateCertificate(cert *x509.Certificate) (bool, string, error) {
	ts.mu.RLock()

	// Check cache first
	cacheKey := string(cert.Raw)
	if entry, exists := ts.validationCache[cacheKey]; exists {
		if time.Now().Before(entry.expiresAt) {
			ts.mu.RUnlock()
			return entry.valid, entry.memberID, nil
		}
	}
	ts.mu.RUnlock()

	// Perform validation
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Try to validate against each member CA
	for memberDomain, memberCA := range ts.memberCAs {
		intermediatePool := x509.NewCertPool()
		intermediatePool.AddCert(memberCA)

		opts := x509.VerifyOptions{
			Roots:         ts.rootPool,
			Intermediates: intermediatePool,
			KeyUsages: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
				x509.ExtKeyUsageClientAuth,
			},
		}

		if _, err := cert.Verify(opts); err == nil {
			// Cache the successful validation
			ts.validationCache[cacheKey] = &validationCacheEntry{
				valid:     true,
				expiresAt: time.Now().Add(ts.cacheTTL),
				memberID:  memberDomain,
			}
			return true, memberDomain, nil
		}
	}

	// Try direct validation against root (for intermediate CAs)
	if cert.IsCA {
		opts := x509.VerifyOptions{
			Roots: ts.rootPool,
			KeyUsages: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
				x509.ExtKeyUsageClientAuth,
			},
		}

		if _, err := cert.Verify(opts); err == nil {
			ts.validationCache[cacheKey] = &validationCacheEntry{
				valid:     true,
				expiresAt: time.Now().Add(ts.cacheTTL),
				memberID:  "root",
			}
			return true, "root", nil
		}
	}

	// Cache the failed validation
	ts.validationCache[cacheKey] = &validationCacheEntry{
		valid:     false,
		expiresAt: time.Now().Add(ts.cacheTTL),
		memberID:  "",
	}

	return false, "", fmt.Errorf("certificate not trusted by any CA in the federation")
}

// GetServerTLSConfig returns a TLS config for servers using the trust store
func (ts *TrustStore) GetServerTLSConfig(certPath, keyPath string) (*tls.Config, error) {
	// Load server certificate and key
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %w", err)
	}

	ts.mu.RLock()
	defer ts.mu.RUnlock()

	// Create a combined pool with root and all member CAs
	certPool := x509.NewCertPool()
	if ts.rootCA != nil {
		certPool.AddCert(ts.rootCA)
	}
	for _, memberCert := range ts.memberCAs {
		certPool.AddCert(memberCert)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
		MinVersion:   tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("no certificates provided")
			}

			peerCert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("failed to parse peer certificate: %w", err)
			}

			valid, memberID, err := ts.ValidateCertificate(peerCert)
			if !valid {
				return fmt.Errorf("certificate validation failed: %w", err)
			}

			// Optional: log which member validated the certificate
			_ = memberID

			return nil
		},
	}, nil
}

// GetClientTLSConfig returns a TLS config for clients using the trust store
func (ts *TrustStore) GetClientTLSConfig(serverName, certPath, keyPath string) (*tls.Config, error) {
	// Load client certificate if provided
	var certificates []tls.Certificate
	if certPath != "" && keyPath != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		certificates = []tls.Certificate{cert}
	}

	ts.mu.RLock()
	defer ts.mu.RUnlock()

	// Create a combined pool with root and all member CAs
	certPool := x509.NewCertPool()
	if ts.rootCA != nil {
		certPool.AddCert(ts.rootCA)
	}
	for _, memberCert := range ts.memberCAs {
		certPool.AddCert(memberCert)
	}

	return &tls.Config{
		ServerName:   serverName,
		Certificates: certificates,
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS12,
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("no certificates provided")
			}

			peerCert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("failed to parse peer certificate: %w", err)
			}

			valid, memberID, err := ts.ValidateCertificate(peerCert)
			if !valid {
				return fmt.Errorf("certificate validation failed: %w", err)
			}

			// Optional: log which member validated the certificate
			_ = memberID

			return nil
		},
	}, nil
}

// GetCertPool returns a cert pool containing all trusted CAs
func (ts *TrustStore) GetCertPool() *x509.CertPool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	pool := x509.NewCertPool()
	if ts.rootCA != nil {
		pool.AddCert(ts.rootCA)
	}
	for _, cert := range ts.memberCAs {
		pool.AddCert(cert)
	}

	return pool
}

// ListMemberCAs returns a list of all loaded member domains
func (ts *TrustStore) ListMemberCAs() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	domains := make([]string, 0, len(ts.memberCAs))
	for domain := range ts.memberCAs {
		domains = append(domains, domain)
	}
	return domains
}

// ClearCache clears the validation cache
func (ts *TrustStore) ClearCache() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.clearCacheLocked()
}

// clearCacheLocked clears the cache (must be called with lock held)
func (ts *TrustStore) clearCacheLocked() {
	ts.validationCache = make(map[string]*validationCacheEntry)
}

// GetMemberCA returns a specific member's CA certificate
func (ts *TrustStore) GetMemberCA(memberDomain string) (*x509.Certificate, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	cert, exists := ts.memberCAs[memberDomain]
	if !exists {
		return nil, fmt.Errorf("member CA for %s not found", memberDomain)
	}

	return cert, nil
}

// GetRootCA returns the federation root CA
func (ts *TrustStore) GetRootCA() *x509.Certificate {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.rootCA
}

// SetCacheTTL sets the TTL for validation cache entries
func (ts *TrustStore) SetCacheTTL(ttl time.Duration) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.cacheTTL = ttl
	ts.clearCacheLocked()
}
