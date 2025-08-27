package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// CertManager implements certificate management with Ed25519 keys
type CertManager struct {
	caPath  string
	caCert  *x509.Certificate
	caKey   ed25519.PrivateKey
}

// NewCertManager creates a new certificate manager
func NewCertManager(caPath string) (*CertManager, error) {
	cm := &CertManager{
		caPath: caPath,
	}
	
	// Try to load existing CA if path exists and files exist
	if caPath != "" {
		certPath := fmt.Sprintf("%s/ca.crt", caPath)
		if _, err := os.Stat(certPath); err == nil {
			// CA files exist, try to load them
			if err := cm.loadCA(); err != nil {
				return nil, fmt.Errorf("failed to load existing CA: %w", err)
			}
		}
	}
	
	return cm, nil
}

// GenerateCA creates a new Certificate Authority with Ed25519
func (cm *CertManager) GenerateCA(memberID string, validity time.Duration) error {
	// Generate Ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}
	
	// Create CA certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"Collective Storage"},
			Country:       []string{"US"},
			Province:      []string{""},
			Locality:      []string{""},
			StreetAddress: []string{""},
			PostalCode:    []string{""},
			CommonName:    fmt.Sprintf("%s-CA", memberID),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	
	// Self-sign the CA certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return fmt.Errorf("failed to create CA certificate: %w", err)
	}
	
	// Parse the certificate
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}
	
	// Store in memory
	cm.caCert = cert
	cm.caKey = priv
	
	// Save to disk if path is set
	if cm.caPath != "" {
		if err := cm.saveCA(certDER, priv); err != nil {
			return fmt.Errorf("failed to save CA: %w", err)
		}
	}
	
	return nil
}

// GenerateCertificate creates a new certificate signed by the CA
func (cm *CertManager) GenerateCertificate(componentType ComponentType, componentID, memberID string, addresses []string, validity time.Duration) (*x509.Certificate, ed25519.PrivateKey, error) {
	if cm.caCert == nil || cm.caKey == nil {
		return nil, nil, fmt.Errorf("CA not initialized")
	}
	
	// Generate Ed25519 key pair for the certificate
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}
	
	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			Organization:  []string{"Collective Storage"},
			Country:       []string{"US"},
			CommonName:    componentID,
		},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	
	// Add Subject Alternative Names for addresses
	for _, addr := range addresses {
		if ip := net.ParseIP(addr); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, addr)
		}
	}
	
	// Add custom extensions for component metadata
	template.ExtraExtensions = []pkix.Extension{
		{
			Id:    []int{1, 2, 3, 4, 5, 1}, // Custom OID for component type
			Value: []byte(componentType),
		},
		{
			Id:    []int{1, 2, 3, 4, 5, 2}, // Custom OID for member ID
			Value: []byte(memberID),
		},
		{
			Id:    []int{1, 2, 3, 4, 5, 3}, // Custom OID for component ID
			Value: []byte(componentID),
		},
	}
	
	// Sign the certificate with the CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, cm.caCert, pub, cm.caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}
	
	// Parse the certificate
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse certificate: %w", err)
	}
	
	return cert, priv, nil
}

// SaveCertificate saves a certificate and key to files
func (cm *CertManager) SaveCertificate(cert *x509.Certificate, key ed25519.PrivateKey, certPath, keyPath string) error {
	// Save certificate
	certFile, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("failed to create certificate file: %w", err)
	}
	defer certFile.Close()
	
	certPEM := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}
	if err := pem.Encode(certFile, certPEM); err != nil {
		return fmt.Errorf("failed to write certificate: %w", err)
	}
	
	// Save private key
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyFile.Close()
	
	privKeyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}
	
	keyPEM := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privKeyBytes,
	}
	if err := pem.Encode(keyFile, keyPEM); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}
	
	return nil
}

// LoadCertificate loads a certificate from a file
func (cm *CertManager) LoadCertificate(path string) (*x509.Certificate, error) {
	certPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate file: %w", err)
	}
	
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to parse certificate PEM")
	}
	
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}
	
	return cert, nil
}

// LoadPrivateKey loads an Ed25519 private key from a file
func (cm *CertManager) LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	keyPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read key file: %w", err)
	}
	
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to parse key PEM")
	}
	
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}
	
	ed25519Key, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not Ed25519")
	}
	
	return ed25519Key, nil
}

// VerifyCertificate verifies a certificate against the CA
func (cm *CertManager) VerifyCertificate(cert *x509.Certificate) error {
	if cm.caCert == nil {
		return fmt.Errorf("CA not initialized")
	}
	
	// Create certificate pool with CA
	roots := x509.NewCertPool()
	roots.AddCert(cm.caCert)
	
	// Verify certificate
	opts := x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	
	if _, err := cert.Verify(opts); err != nil {
		return fmt.Errorf("certificate verification failed: %w", err)
	}
	
	return nil
}

// GetIdentityFromCert extracts identity information from a certificate
func (cm *CertManager) GetIdentityFromCert(cert *x509.Certificate) (*Identity, error) {
	identity := &Identity{
		Subject:      cert.Subject.CommonName,
		Issuer:       cert.Issuer.CommonName,
		SerialNumber: cert.SerialNumber.String(),
		NotBefore:    cert.NotBefore,
		NotAfter:     cert.NotAfter,
	}
	
	// Extract custom extensions
	for _, ext := range cert.Extensions {
		switch {
		case ext.Id.Equal([]int{1, 2, 3, 4, 5, 1}):
			identity.Type = ComponentType(ext.Value)
		case ext.Id.Equal([]int{1, 2, 3, 4, 5, 2}):
			identity.MemberID = string(ext.Value)
		case ext.Id.Equal([]int{1, 2, 3, 4, 5, 3}):
			identity.ComponentID = string(ext.Value)
		}
	}
	
	// Extract addresses from SAN
	for _, ip := range cert.IPAddresses {
		identity.Addresses = append(identity.Addresses, ip.String())
	}
	for _, dns := range cert.DNSNames {
		identity.Addresses = append(identity.Addresses, dns)
	}
	
	return identity, nil
}

// loadCA loads the CA certificate and key from disk
func (cm *CertManager) loadCA() error {
	certPath := fmt.Sprintf("%s/ca.crt", cm.caPath)
	keyPath := fmt.Sprintf("%s/ca.key", cm.caPath)
	
	cert, err := cm.LoadCertificate(certPath)
	if err != nil {
		return err
	}
	
	key, err := cm.LoadPrivateKey(keyPath)
	if err != nil {
		return err
	}
	
	cm.caCert = cert
	cm.caKey = key
	
	return nil
}

// saveCA saves the CA certificate and key to disk
func (cm *CertManager) saveCA(certDER []byte, key ed25519.PrivateKey) error {
	// Create directory if it doesn't exist
	if err := os.MkdirAll(cm.caPath, 0700); err != nil {
		return fmt.Errorf("failed to create CA directory: %w", err)
	}
	
	certPath := fmt.Sprintf("%s/ca.crt", cm.caPath)
	keyPath := fmt.Sprintf("%s/ca.key", cm.caPath)
	
	// Save certificate
	certFile, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("failed to create CA certificate file: %w", err)
	}
	defer certFile.Close()
	
	certPEM := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	}
	if err := pem.Encode(certFile, certPEM); err != nil {
		return fmt.Errorf("failed to write CA certificate: %w", err)
	}
	
	// Save private key with restricted permissions
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create CA key file: %w", err)
	}
	defer keyFile.Close()
	
	privKeyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal CA private key: %w", err)
	}
	
	keyPEM := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privKeyBytes,
	}
	if err := pem.Encode(keyFile, keyPEM); err != nil {
		return fmt.Errorf("failed to write CA private key: %w", err)
	}
	
	return nil
}