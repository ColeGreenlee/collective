package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"
)

var (
	ErrInvalidCertificate = errors.New("invalid certificate")
	ErrCertificateExpired = errors.New("certificate expired")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrInvalidCA          = errors.New("invalid CA certificate")
)

// ComponentType identifies the type of component in the system
type ComponentType string

const (
	ComponentCoordinator ComponentType = "coordinator"
	ComponentNode        ComponentType = "node"
	ComponentClient      ComponentType = "client"
)

// Identity represents an authenticated entity in the system
type Identity struct {
	// Core identity fields
	Type        ComponentType
	MemberID    string
	ComponentID string // NodeID for nodes, CoordinatorID for coordinators, ClientID for clients

	// Certificate details
	Subject      string
	Issuer       string
	SerialNumber string
	NotBefore    time.Time
	NotAfter     time.Time

	// Additional attributes
	Addresses   []string // Valid addresses for this identity
	Permissions []string // Permissions granted to this identity
}

// Authenticator provides authentication services
type Authenticator interface {
	// ValidatePeerCertificate validates a peer's certificate during TLS handshake
	ValidatePeerCertificate(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error

	// GetIdentity extracts identity from a certificate
	GetIdentity(cert *x509.Certificate) (*Identity, error)

	// ValidateIdentity checks if an identity is valid and authorized
	ValidateIdentity(ctx context.Context, identity *Identity) error

	// GetTLSConfig returns TLS configuration for this authenticator
	GetTLSConfig() (*tls.Config, error)
}

// CertificateManager handles certificate lifecycle
type CertificateManager interface {
	// GenerateCA creates a new Certificate Authority
	GenerateCA(memberID string, validity time.Duration) error

	// GenerateCertificate creates a new certificate
	GenerateCertificate(componentType ComponentType, componentID, memberID string, addresses []string, validity time.Duration) error

	// LoadCertificate loads a certificate from storage
	LoadCertificate(path string) (*x509.Certificate, error)

	// SaveCertificate saves a certificate to storage
	SaveCertificate(cert *x509.Certificate, keyPath, certPath string) error

	// RotateCertificate rotates an existing certificate
	RotateCertificate(oldCertPath, oldKeyPath string) error

	// VerifyCertificate verifies a certificate against a CA
	VerifyCertificate(cert *x509.Certificate, ca *x509.Certificate) error

	// GetExpiryTime returns the expiry time of a certificate
	GetExpiryTime(certPath string) (time.Time, error)
}

// TokenManager handles token-based authentication
type TokenManager interface {
	// GenerateToken creates a new authentication token
	GenerateToken(identity *Identity, validity time.Duration) (string, error)

	// ValidateToken validates and extracts identity from a token
	ValidateToken(token string) (*Identity, error)

	// RevokeToken revokes a token
	RevokeToken(token string) error

	// IsRevoked checks if a token has been revoked
	IsRevoked(token string) bool
}

// AuthConfig holds authentication configuration
type AuthConfig struct {
	Enabled           bool     `json:"enabled"`
	CAPath            string   `json:"ca_cert"`
	CertPath          string   `json:"cert"`
	KeyPath           string   `json:"key"`
	ClientCAPath      string   `json:"client_ca,omitempty"`
	PeerVerification  bool     `json:"peer_verification"`
	RequireClientAuth bool     `json:"require_client_auth"`
	AllowedMemberIDs  []string `json:"allowed_member_ids,omitempty"`
	TokenSigningKey   string   `json:"token_signing_key,omitempty"`
	MinTLSVersion     string   `json:"min_tls_version,omitempty"`
}

// DefaultAuthConfig returns default authentication configuration
func DefaultAuthConfig() *AuthConfig {
	return &AuthConfig{
		Enabled:           false,
		PeerVerification:  true,
		RequireClientAuth: false,
		MinTLSVersion:     "1.2",
	}
}

// Validate checks if the authentication configuration is valid
func (c *AuthConfig) Validate() error {
	if !c.Enabled {
		return nil
	}

	if c.CAPath == "" {
		return errors.New("CA certificate path is required when authentication is enabled")
	}

	if c.CertPath == "" || c.KeyPath == "" {
		return errors.New("certificate and key paths are required when authentication is enabled")
	}

	return nil
}
