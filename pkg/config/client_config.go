package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClientConfig represents the client-side configuration for collective CLI
type ClientConfig struct {
	CurrentContext string          `json:"current_context"`
	Contexts       []Context       `json:"contexts"`
	Defaults       DefaultSettings `json:"defaults"`
}

// Context represents a connection context to a collective
type Context struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Coordinator string       `json:"coordinator"`
	MemberID    string       `json:"member_id"`
	Auth        AuthSettings `json:"auth"`
}

// AuthSettings represents authentication settings for a context
type AuthSettings struct {
	Type     string `json:"type"` // "certificate" or "token"
	CAPath   string `json:"ca_cert,omitempty"`
	CertPath string `json:"client_cert,omitempty"`
	KeyPath  string `json:"client_key,omitempty"`
	Token    string `json:"token,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// DefaultSettings represents default settings for the client
type DefaultSettings struct {
	Timeout      string `json:"timeout,omitempty"`
	RetryCount   int    `json:"retry_count,omitempty"`
	OutputFormat string `json:"output_format,omitempty"`
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

// LoadClientConfig loads the client configuration from file
func LoadClientConfig() (*ClientConfig, error) {
	configPath := GetConfigPath()

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Return empty config if doesn't exist
		return &ClientConfig{
			Defaults: DefaultSettings{
				Timeout:      "30s",
				RetryCount:   3,
				OutputFormat: "styled",
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

	// Expand paths
	for i := range config.Contexts {
		config.Contexts[i].Auth.CAPath = expandPath(config.Contexts[i].Auth.CAPath)
		config.Contexts[i].Auth.CertPath = expandPath(config.Contexts[i].Auth.CertPath)
		config.Contexts[i].Auth.KeyPath = expandPath(config.Contexts[i].Auth.KeyPath)
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

// GetContext returns the context with the given name
func (c *ClientConfig) GetContext(name string) (*Context, error) {
	for _, ctx := range c.Contexts {
		if ctx.Name == name {
			return &ctx, nil
		}
	}
	return nil, fmt.Errorf("context %q not found", name)
}

// GetCurrentContext returns the current context
func (c *ClientConfig) GetCurrentContext() (*Context, error) {
	if c.CurrentContext == "" {
		return nil, fmt.Errorf("no current context set")
	}
	return c.GetContext(c.CurrentContext)
}

// AddContext adds a new context or updates an existing one
func (c *ClientConfig) AddContext(ctx Context) error {
	// Validate context
	if ctx.Name == "" {
		return fmt.Errorf("context name is required")
	}
	if ctx.Coordinator == "" {
		return fmt.Errorf("coordinator address is required")
	}

	// Check if context exists
	for i, existing := range c.Contexts {
		if existing.Name == ctx.Name {
			// Update existing context
			c.Contexts[i] = ctx
			return c.Save()
		}
	}

	// Add new context
	c.Contexts = append(c.Contexts, ctx)

	// Set as current if it's the first context
	if c.CurrentContext == "" {
		c.CurrentContext = ctx.Name
	}

	return c.Save()
}

// RemoveContext removes a context
func (c *ClientConfig) RemoveContext(name string) error {
	for i, ctx := range c.Contexts {
		if ctx.Name == name {
			// Remove from slice
			c.Contexts = append(c.Contexts[:i], c.Contexts[i+1:]...)

			// Update current context if needed
			if c.CurrentContext == name {
				c.CurrentContext = ""
				if len(c.Contexts) > 0 {
					c.CurrentContext = c.Contexts[0].Name
				}
			}

			return c.Save()
		}
	}
	return fmt.Errorf("context %q not found", name)
}

// SetCurrentContext sets the current context
func (c *ClientConfig) SetCurrentContext(name string) error {
	// Verify context exists
	if _, err := c.GetContext(name); err != nil {
		return err
	}

	c.CurrentContext = name
	return c.Save()
}

// GetCertificateDir returns the certificate directory for a context
func GetCertificateDir(contextName string) string {
	return filepath.Join(GetConfigDir(), "certificates", contextName)
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

// GetConnectionConfig returns the connection configuration for the current context
func (c *ClientConfig) GetConnectionConfig(contextOverride string) (*ConnectionConfig, error) {
	var ctx *Context
	var err error

	if contextOverride != "" {
		ctx, err = c.GetContext(contextOverride)
	} else {
		ctx, err = c.GetCurrentContext()
	}

	if err != nil {
		return nil, err
	}

	// Parse timeout
	timeout := 30 * time.Second
	if c.Defaults.Timeout != "" {
		if t, err := time.ParseDuration(c.Defaults.Timeout); err == nil {
			timeout = t
		}
	}

	return &ConnectionConfig{
		Coordinator: ctx.Coordinator,
		CAPath:      ctx.Auth.CAPath,
		CertPath:    ctx.Auth.CertPath,
		KeyPath:     ctx.Auth.KeyPath,
		Insecure:    ctx.Auth.Insecure,
		Timeout:     timeout,
	}, nil
}

// InitializeContext creates a new context with auto-generated certificates
func InitializeContext(name, coordinator, memberID, caPath string) error {
	config, err := LoadClientConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create certificate directory
	certDir := GetCertificateDir(name)
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return fmt.Errorf("failed to create certificate directory: %w", err)
	}

	// Set up paths
	caCertPath := filepath.Join(certDir, "ca.crt")
	clientCertPath := filepath.Join(certDir, "client.crt")
	clientKeyPath := filepath.Join(certDir, "client.key")

	// Copy CA certificate if provided
	if caPath != "" {
		caData, err := os.ReadFile(expandPath(caPath))
		if err != nil {
			return fmt.Errorf("failed to read CA certificate: %w", err)
		}
		if err := os.WriteFile(caCertPath, caData, 0644); err != nil {
			return fmt.Errorf("failed to write CA certificate: %w", err)
		}
	}

	// Create context
	ctx := Context{
		Name:        name,
		Coordinator: coordinator,
		MemberID:    memberID,
		Auth: AuthSettings{
			Type:     "certificate",
			CAPath:   caCertPath,
			CertPath: clientCertPath,
			KeyPath:  clientKeyPath,
		},
	}

	// Add context to config
	if err := config.AddContext(ctx); err != nil {
		return fmt.Errorf("failed to add context: %w", err)
	}

	fmt.Printf("✓ Context %q created successfully\n", name)
	fmt.Printf("  Coordinator: %s\n", coordinator)
	fmt.Printf("  Member ID: %s\n", memberID)
	fmt.Printf("  Certificates: %s\n", certDir)

	return nil
}
