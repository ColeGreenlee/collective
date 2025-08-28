package config

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClientConfig represents the unified client configuration for collective CLI
type ClientConfig struct {
	Version      string           `json:"version"`
	UserIdentity *UserIdentity    `json:"user_identity,omitempty"`
	Collectives  []CollectiveInfo `json:"collectives"`
	Defaults     DefaultSettings  `json:"defaults"`
	
}


// DefaultSettings represents default settings for the client
type DefaultSettings struct {
	PreferredCollective string `json:"preferred_collective,omitempty"`
	AutoDiscovery       bool   `json:"auto_discovery"`
	Timeout             string `json:"timeout,omitempty"`
	RetryCount          int    `json:"retry_count,omitempty"`
	OutputFormat        string `json:"output_format,omitempty"`
}

// UserIdentity represents a global user identity across collectives
type UserIdentity struct {
	GlobalID    string    `json:"global_id"`    // e.g., "alice@collective.network"
	DisplayName string    `json:"display_name"` // e.g., "Alice Johnson"
	PublicKey   string    `json:"public_key"`   // Ed25519 public key
	Created     time.Time `json:"created"`
}

// CollectiveInfo represents connection info for a specific collective
type CollectiveInfo struct {
	CollectiveID        string            `json:"collective_id"`        // e.g., "home.alice"
	CoordinatorAddress  string            `json:"coordinator_address"`  // e.g., "alice.collective:8001"
	MemberID            string            `json:"member_id"`            // Local member ID in this collective
	Role                string            `json:"role"`                 // "owner", "member", "guest"
	Certificates        CertificateConfig `json:"certificates"`
	Permissions         []string          `json:"permissions,omitempty"` // Optional permission restrictions
	AutoDiscover        bool              `json:"auto_discover"`         // Whether to auto-discover this collective
	TrustLevel          string            `json:"trust_level"`           // "high", "medium", "low"
	Description         string            `json:"description,omitempty"`
	Tags                []string          `json:"tags,omitempty"`
}

// CertificateConfig represents certificate paths for a collective
type CertificateConfig struct {
	CACert     string `json:"ca_cert"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

// GetConfigDir returns the collective configuration directory
func GetConfigDir() string {
	// Check environment variable first
	if dir := os.Getenv("COLLECTIVE_CONFIG_DIR"); dir != "" {
		return dir
	}

	// Use XDG_CONFIG_HOME if set
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "collective")
	}

	// Default to ~/.collective
	home, err := os.UserHomeDir()
	if err != nil {
		return ".collective" // Fallback to current directory
	}
	return filepath.Join(home, ".collective")
}

// GetConfigPath returns the path to the main config file
func GetConfigPath() string {
	return filepath.Join(GetConfigDir(), "config.json")
}

// LoadClientConfig loads the unified client configuration from file
func LoadClientConfig() (*ClientConfig, error) {
	configPath := GetConfigPath()

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return empty config with defaults
		return &ClientConfig{
			Version: "2.0",
			Defaults: DefaultSettings{
				AutoDiscovery: true,
				Timeout:       "30s",
				RetryCount:    3,
				OutputFormat:  "styled",
			},
		}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config ClientConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set version if not present (for migration)
	if config.Version == "" {
		config.Version = "2.0"
	}

	// Expand certificate paths for collectives
	for i := range config.Collectives {
		config.Collectives[i].Certificates.CACert = expandPath(config.Collectives[i].Certificates.CACert)
		config.Collectives[i].Certificates.ClientCert = expandPath(config.Collectives[i].Certificates.ClientCert)
		config.Collectives[i].Certificates.ClientKey = expandPath(config.Collectives[i].Certificates.ClientKey)
	}


	return &config, nil
}

// SaveClientConfig saves the client configuration to file
func (c *ClientConfig) Save() error {
	configDir := GetConfigDir()
	configPath := GetConfigPath()

	// Create directory if it doesn't exist
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal config to JSON
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file with restricted permissions
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}



// expandPath expands ~ and environment variables in paths
func expandPath(path string) string {
	if path == "" {
		return path
	}

	// Expand ~
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	// Expand environment variables
	return os.ExpandEnv(path)
}

// ConnectionConfig represents the resolved connection configuration
type ConnectionConfig struct {
	Coordinator string
	CAPath      string
	CertPath    string
	KeyPath     string
	Insecure    bool
	Timeout     time.Duration
}

// GetCollectiveByID returns collective info by collective ID
func (c *ClientConfig) GetCollectiveByID(collectiveID string) (*CollectiveInfo, error) {
	for _, collective := range c.Collectives {
		if collective.CollectiveID == collectiveID {
			return &collective, nil
		}
	}
	return nil, fmt.Errorf("collective %q not found", collectiveID)
}

// GetCollectiveByCoordinator returns collective info by coordinator address
func (c *ClientConfig) GetCollectiveByCoordinator(coordinatorAddr string) (*CollectiveInfo, error) {
	// Normalize address for comparison
	normalizedAddr := normalizeAddress(coordinatorAddr)
	
	for _, collective := range c.Collectives {
		if normalizeAddress(collective.CoordinatorAddress) == normalizedAddr {
			return &collective, nil
		}
	}
	return nil, fmt.Errorf("collective with coordinator %q not found", coordinatorAddr)
}

// GetPreferredCollective returns the preferred collective or the first available one
func (c *ClientConfig) GetPreferredCollective() (*CollectiveInfo, error) {
	if c.Defaults.PreferredCollective != "" {
		return c.GetCollectiveByID(c.Defaults.PreferredCollective)
	}
	
	if len(c.Collectives) == 0 {
		return nil, fmt.Errorf("no collectives configured")
	}
	
	return &c.Collectives[0], nil
}

// AddCollective adds or updates a collective in the configuration
func (c *ClientConfig) AddCollective(collective CollectiveInfo) error {
	// Validate collective
	if collective.CollectiveID == "" {
		return fmt.Errorf("collective ID is required")
	}
	if collective.CoordinatorAddress == "" {
		return fmt.Errorf("coordinator address is required")
	}

	// Check if collective exists
	for i, existing := range c.Collectives {
		if existing.CollectiveID == collective.CollectiveID {
			// Update existing collective
			c.Collectives[i] = collective
			return c.Save()
		}
	}

	// Add new collective
	c.Collectives = append(c.Collectives, collective)

	// Set as preferred if it's the first collective
	if c.Defaults.PreferredCollective == "" {
		c.Defaults.PreferredCollective = collective.CollectiveID
	}

	return c.Save()
}

// RemoveCollective removes a collective from the configuration
func (c *ClientConfig) RemoveCollective(collectiveID string) error {
	for i, collective := range c.Collectives {
		if collective.CollectiveID == collectiveID {
			// Remove from slice
			c.Collectives = append(c.Collectives[:i], c.Collectives[i+1:]...)

			// Update preferred collective if needed
			if c.Defaults.PreferredCollective == collectiveID {
				c.Defaults.PreferredCollective = ""
				if len(c.Collectives) > 0 {
					c.Defaults.PreferredCollective = c.Collectives[0].CollectiveID
				}
			}

			return c.Save()
		}
	}
	return fmt.Errorf("collective %q not found", collectiveID)
}

// ResolveConnection resolves connection configuration for a given coordinator address
func (c *ClientConfig) ResolveConnection(coordinatorAddr string) (*ConnectionConfig, error) {
	// If no coordinator specified, use preferred collective
	if coordinatorAddr == "" {
		collective, err := c.GetPreferredCollective()
		if err != nil {
			return nil, fmt.Errorf("failed to get preferred collective: %w", err)
		}
		coordinatorAddr = collective.CoordinatorAddress
	}

	// Find collective by coordinator address
	collective, err := c.GetCollectiveByCoordinator(coordinatorAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve collective: %w", err)
	}

	// Parse timeout
	timeout := 30 * time.Second
	if c.Defaults.Timeout != "" {
		if t, err := time.ParseDuration(c.Defaults.Timeout); err == nil {
			timeout = t
		}
	}

	return &ConnectionConfig{
		Coordinator: collective.CoordinatorAddress,
		CAPath:      collective.Certificates.CACert,
		CertPath:    collective.Certificates.ClientCert,
		KeyPath:     collective.Certificates.ClientKey,
		Insecure:    false, // Always use secure connections in federated mode
		Timeout:     timeout,
	}, nil
}



// normalizeAddress normalizes a coordinator address for comparison
func normalizeAddress(addr string) string {
	// Add default port if missing
	if !strings.Contains(addr, ":") {
		addr = addr + ":8001"
	}
	
	// Resolve hostname to handle localhost, 127.0.0.1, etc.
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return fmt.Sprintf("localhost:%s", port)
		}
	}
	
	return addr
}

// CreateUserIdentity creates a new user identity with generated key pair
func CreateUserIdentity(globalID, displayName string) (*UserIdentity, ed25519.PrivateKey, error) {
	// Generate Ed25519 key pair
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	identity := &UserIdentity{
		GlobalID:    globalID,
		DisplayName: displayName,
		PublicKey:   hex.EncodeToString(publicKey),
		Created:     time.Now(),
	}

	return identity, privateKey, nil
}

// GetCollectiveCertificateDir returns the certificate directory for a collective
func GetCollectiveCertificateDir(collectiveID string) string {
	return filepath.Join(GetConfigDir(), "certs", collectiveID)
}
