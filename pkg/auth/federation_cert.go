package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Federation OID definitions for certificate extensions
var (
	OIDFederationDomain = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1} // collective.local
	OIDMemberDomain     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 2} // alice.collective.local
	OIDComponentType    = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 3} // coordinator|node|client
	OIDFederatedAddress = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 4} // full address
)

// FederationCertManager extends CertManager with federation capabilities
type FederationCertManager struct {
	*CertManager
	// Override fields for different key types
	federationCACert *x509.Certificate
	federationCAKey  interface{} // Support multiple key types
}

// NewFederationCertManager creates a new federation-aware certificate manager
func NewFederationCertManager(caPath string) *FederationCertManager {
	return &FederationCertManager{
		CertManager: &CertManager{
			caPath: caPath,
		},
	}
}

// GenerateFederationRootCA creates a federation root CA with ED25519
func (fcm *FederationCertManager) GenerateFederationRootCA(federationDomain string, validity time.Duration) error {
	// Generate Ed25519 key pair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}

	// Encode federation domain for extension
	federationDomainExt, err := asn1.Marshal(federationDomain)
	if err != nil {
		return fmt.Errorf("failed to marshal federation domain: %w", err)
	}

	// Create root CA certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization:  []string{"Collective Storage Federation"},
			Country:       []string{"US"},
			CommonName:    fmt.Sprintf("%s Root CA", federationDomain),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            2, // Root -> Intermediate -> Leaf
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		// Add federation domain extension
		ExtraExtensions: []pkix.Extension{
			{
				Id:       OIDFederationDomain,
				Critical: false,
				Value:    federationDomainExt,
			},
		},
	}

	// Self-sign the root CA certificate
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return fmt.Errorf("failed to create root CA certificate: %w", err)
	}

	// Parse the certificate
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse root CA certificate: %w", err)
	}

	// Store in memory
	fcm.caCert = cert
	fcm.caKey = priv

	// Save to disk
	if fcm.caPath != "" {
		if err := fcm.saveCA(certDER, priv); err != nil {
			return fmt.Errorf("failed to save root CA: %w", err)
		}
	}

	return nil
}

// GenerateMemberCA generates an intermediate CA for a federation member
func (fcm *FederationCertManager) GenerateMemberCA(memberDomain string, validity time.Duration, outputPath string) error {
	if fcm.caCert == nil || fcm.caKey == nil {
		return fmt.Errorf("root CA not initialized")
	}

	// Generate Ed25519 key pair for member CA
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}

	// Encode member domain for extension
	memberDomainExt, err := asn1.Marshal(memberDomain)
	if err != nil {
		return fmt.Errorf("failed to marshal member domain: %w", err)
	}

	// Get federation domain from root CA
	var federationDomain string
	for _, ext := range fcm.caCert.Extensions {
		if ext.Id.Equal(OIDFederationDomain) {
			asn1.Unmarshal(ext.Value, &federationDomain)
			break
		}
	}

	// Create intermediate CA certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			Organization:  []string{"Collective Storage Federation"},
			Country:       []string{"US"},
			CommonName:    fmt.Sprintf("%s Intermediate CA", memberDomain),
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1, // Can sign leaf certificates
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		// Add member domain extension
		ExtraExtensions: []pkix.Extension{
			{
				Id:       OIDMemberDomain,
				Critical: false,
				Value:    memberDomainExt,
			},
		},
	}

	// If federation domain found, propagate it
	if federationDomain != "" {
		federationDomainExt, _ := asn1.Marshal(federationDomain)
		template.ExtraExtensions = append(template.ExtraExtensions, pkix.Extension{
			Id:       OIDFederationDomain,
			Critical: false,
			Value:    federationDomainExt,
		})
	}

	// Sign with root CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, fcm.caCert, pub, fcm.caKey)
	if err != nil {
		return fmt.Errorf("failed to create member CA certificate: %w", err)
	}

	// Parse the certificate
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse member CA certificate: %w", err)
	}

	// Save certificate and key
	outputDir := filepath.Dir(outputPath)
	if outputDir != "." && outputDir != "" {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	// Save certificate
	certFile, err := os.Create(outputPath)
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

	// Save private key (replace .crt with .key)
	keyPath := outputPath[:len(outputPath)-4] + ".key"
	keyFile, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyFile.Close()

	privKeyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
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

// SignMemberCSR signs a member's CSR with the root CA
func (fcm *FederationCertManager) SignMemberCSR(csrPath, memberDomain string, validity time.Duration, outputPath string) error {
	if fcm.caCert == nil || fcm.caKey == nil {
		return fmt.Errorf("root CA not initialized")
	}

	// Read CSR file
	csrPEM, err := ioutil.ReadFile(csrPath)
	if err != nil {
		return fmt.Errorf("failed to read CSR file: %w", err)
	}

	// Parse CSR
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return fmt.Errorf("failed to parse CSR PEM")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CSR: %w", err)
	}

	// Verify CSR signature
	if err := csr.CheckSignature(); err != nil {
		return fmt.Errorf("CSR signature verification failed: %w", err)
	}

	// Encode member domain for extension
	memberDomainExt, err := asn1.Marshal(memberDomain)
	if err != nil {
		return fmt.Errorf("failed to marshal member domain: %w", err)
	}

	// Create certificate template from CSR
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().Unix()),
		Subject:               csr.Subject,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            1, // Intermediate CA can sign leaf certs
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:              csr.DNSNames,
		IPAddresses:           csr.IPAddresses,
		// Add member domain extension
		ExtraExtensions: []pkix.Extension{
			{
				Id:       OIDMemberDomain,
				Critical: false,
				Value:    memberDomainExt,
			},
		},
	}

	// Sign with root CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, fcm.caCert, csr.PublicKey, fcm.caKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Save certificate
	certFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create certificate file: %w", err)
	}
	defer certFile.Close()

	certPEM := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	}
	if err := pem.Encode(certFile, certPEM); err != nil {
		return fmt.Errorf("failed to write certificate: %w", err)
	}

	return nil
}

// LoadCA loads an existing CA from disk
func (fcm *FederationCertManager) LoadCA() error {
	return fcm.CertManager.loadCA()
}

// LoadCAFromFiles loads CA certificate and key from specific file paths
func (fcm *FederationCertManager) LoadCAFromFiles(certPath, keyPath string) error {
	// Load certificate
	certPEM, err := ioutil.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return fmt.Errorf("failed to parse CA certificate PEM")
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	if !cert.IsCA {
		return fmt.Errorf("certificate is not a CA certificate")
	}

	// Load private key
	keyPEM, err := ioutil.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read CA private key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return fmt.Errorf("failed to parse CA private key PEM")
	}

	var privKey interface{}
	switch keyBlock.Type {
	case "PRIVATE KEY":
		privKey, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	case "RSA PRIVATE KEY":
		privKey, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
	case "EC PRIVATE KEY":
		privKey, err = x509.ParseECPrivateKey(keyBlock.Bytes)
	default:
		return fmt.Errorf("unsupported private key type: %s", keyBlock.Type)
	}

	if err != nil {
		return fmt.Errorf("failed to parse CA private key: %w", err)
	}

	// Store in memory
	fcm.federationCACert = cert
	fcm.federationCAKey = privKey

	return nil
}

// GetCACert returns the loaded CA certificate
func (fcm *FederationCertManager) GetCACert() *x509.Certificate {
	return fcm.federationCACert
}

// GetCAKey returns the loaded CA private key
func (fcm *FederationCertManager) GetCAKey() interface{} {
	return fcm.federationCAKey
}

// VerifyChain verifies a certificate chain from leaf to root
func (fcm *FederationCertManager) VerifyChain(certPath string) error {
	if fcm.caCert == nil {
		return fmt.Errorf("root CA not loaded")
	}

	// Read certificate file
	certPEM, err := ioutil.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate: %w", err)
	}

	// Parse certificate
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("failed to parse certificate PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Create certificate pool with root CA
	rootPool := x509.NewCertPool()
	rootPool.AddCert(fcm.caCert)

	// If this is an intermediate CA, verify against root
	if cert.IsCA {
		opts := x509.VerifyOptions{
			Roots: rootPool,
			KeyUsages: []x509.ExtKeyUsage{
				x509.ExtKeyUsageServerAuth,
				x509.ExtKeyUsageClientAuth,
			},
		}

		if _, err := cert.Verify(opts); err != nil {
			return fmt.Errorf("certificate chain verification failed: %w", err)
		}
	} else {
		// For leaf certificates, we'd need the intermediate CA as well
		// This is a simplified version
		return fmt.Errorf("leaf certificate verification not yet implemented")
	}

	return nil
}

// GetFederationDomain extracts the federation domain from a certificate
func GetFederationDomain(cert *x509.Certificate) (string, error) {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(OIDFederationDomain) {
			var domain string
			_, err := asn1.Unmarshal(ext.Value, &domain)
			if err != nil {
				return "", fmt.Errorf("failed to unmarshal federation domain: %w", err)
			}
			return domain, nil
		}
	}
	return "", fmt.Errorf("federation domain extension not found")
}

// GetMemberDomain extracts the member domain from a certificate
func GetMemberDomain(cert *x509.Certificate) (string, error) {
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(OIDMemberDomain) {
			var domain string
			_, err := asn1.Unmarshal(ext.Value, &domain)
			if err != nil {
				return "", fmt.Errorf("failed to unmarshal member domain: %w", err)
			}
			return domain, nil
		}
	}
	return "", fmt.Errorf("member domain extension not found")
}