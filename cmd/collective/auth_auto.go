package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"collective/pkg/auth"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

var authAutoInitCmd = &cobra.Command{
	Use:   "auto-init",
	Short: "Automatically initialize certificates for a component",
	Long:  "Automatically generate CA and component certificates based on environment configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get configuration from flags or environment
		memberID, _ := cmd.Flags().GetString("member-id")
		componentType, _ := cmd.Flags().GetString("component-type")
		componentID, _ := cmd.Flags().GetString("component-id")
		certDir, _ := cmd.Flags().GetString("cert-dir")
		caValidity, _ := cmd.Flags().GetDuration("ca-validity")
		certValidity, _ := cmd.Flags().GetDuration("cert-validity")
		force, _ := cmd.Flags().GetBool("force")

		// Use environment variables if flags not provided
		if memberID == "" {
			memberID = os.Getenv("COLLECTIVE_MEMBER_ID")
		}
		if componentType == "" {
			componentType = os.Getenv("COLLECTIVE_COMPONENT_TYPE")
		}
		if componentID == "" {
			componentID = os.Getenv("COLLECTIVE_COMPONENT_ID")
		}
		if certDir == "" {
			certDir = os.Getenv("COLLECTIVE_CERT_DIR")
			if certDir == "" {
				certDir = "/collective/certs"
			}
		}

		// Validate required parameters
		if memberID == "" {
			return fmt.Errorf("member-id is required (set via flag or COLLECTIVE_MEMBER_ID env)")
		}
		if componentType == "" {
			return fmt.Errorf("component-type is required (set via flag or COLLECTIVE_COMPONENT_TYPE env)")
		}

		// Auto-generate component ID if not provided
		if componentID == "" {
			hostname, _ := os.Hostname()
			if hostname == "" {
				hostname = fmt.Sprintf("auto-%d", os.Getpid())
			}
			componentID = fmt.Sprintf("%s-%s", memberID, hostname)
		}

		// Setup logger
		logger, _ := zap.NewProduction()
		defer logger.Sync()

		logger.Info("Auto-initializing certificates",
			zap.String("member_id", memberID),
			zap.String("component_type", componentType),
			zap.String("component_id", componentID),
			zap.String("cert_dir", certDir))

		// Create directory structure
		caDir := filepath.Join(certDir, "ca")
		componentDir := filepath.Join(certDir, componentType+"s")

		if err := os.MkdirAll(caDir, 0755); err != nil {
			return fmt.Errorf("failed to create CA directory: %w", err)
		}
		if err := os.MkdirAll(componentDir, 0755); err != nil {
			return fmt.Errorf("failed to create component directory: %w", err)
		}

		// Step 1: Check/Create CA
		caCertPath := filepath.Join(caDir, fmt.Sprintf("%s-ca.crt", memberID))
		caKeyPath := filepath.Join(caDir, fmt.Sprintf("%s-ca.key", memberID))

		caExists := fileExists(caCertPath) && fileExists(caKeyPath)

		if !caExists || force {
			logger.Info("Generating new CA for member", zap.String("member_id", memberID))

			// Create certificate manager with CA directory
			certManager, err := auth.NewCertManager(filepath.Join(caDir, memberID))
			if err != nil {
				return fmt.Errorf("failed to create certificate manager: %w", err)
			}

			// Generate CA
			if err := certManager.GenerateCA(memberID, caValidity); err != nil {
				return fmt.Errorf("failed to generate CA: %w", err)
			}

			// Move generated CA files to standard location
			generatedCAPath := filepath.Join(caDir, memberID)
			if err := os.Rename(filepath.Join(generatedCAPath, "ca.crt"), caCertPath); err != nil {
				return fmt.Errorf("failed to move CA certificate: %w", err)
			}
			if err := os.Rename(filepath.Join(generatedCAPath, "ca.key"), caKeyPath); err != nil {
				return fmt.Errorf("failed to move CA key: %w", err)
			}
			os.RemoveAll(generatedCAPath) // Clean up temp directory

			// Secure the CA key
			if err := os.Chmod(caKeyPath, 0600); err != nil {
				return fmt.Errorf("failed to secure CA key: %w", err)
			}

			fmt.Printf("✓ CA generated for member '%s'\n", memberID)
		} else {
			logger.Info("Using existing CA", zap.String("member_id", memberID))
			fmt.Printf("✓ Using existing CA for member '%s'\n", memberID)
		}

		// Step 2: Generate component certificate
		certPath := filepath.Join(componentDir, fmt.Sprintf("%s.crt", componentID))
		keyPath := filepath.Join(componentDir, fmt.Sprintf("%s.key", componentID))

		certExists := fileExists(certPath) && fileExists(keyPath)

		if !certExists || force {
			logger.Info("Generating certificate for component",
				zap.String("component_type", componentType),
				zap.String("component_id", componentID))

			// Create a temporary directory structure for the cert manager
			tempCADir := filepath.Join(caDir, "temp-"+memberID)
			os.MkdirAll(tempCADir, 0755)
			defer os.RemoveAll(tempCADir)

			// Copy CA files to temp directory (cert manager expects specific structure)
			caCertData, err := os.ReadFile(caCertPath)
			if err != nil {
				return fmt.Errorf("failed to read CA certificate: %w", err)
			}
			caKeyData, err := os.ReadFile(caKeyPath)
			if err != nil {
				return fmt.Errorf("failed to read CA key: %w", err)
			}

			if err := os.WriteFile(filepath.Join(tempCADir, "ca.crt"), caCertData, 0644); err != nil {
				return fmt.Errorf("failed to copy CA certificate: %w", err)
			}
			if err := os.WriteFile(filepath.Join(tempCADir, "ca.key"), caKeyData, 0600); err != nil {
				return fmt.Errorf("failed to copy CA key: %w", err)
			}

			certManager, err := auth.NewCertManager(tempCADir)
			if err != nil {
				return fmt.Errorf("failed to create certificate manager: %w", err)
			}

			// Determine component type for certificate
			var certType auth.ComponentType
			switch strings.ToLower(componentType) {
			case "coordinator":
				certType = auth.ComponentCoordinator
			case "node":
				certType = auth.ComponentNode
			case "client":
				certType = auth.ComponentClient
			default:
				return fmt.Errorf("invalid component type: %s (use coordinator, node, or client)", componentType)
			}

			// Generate addresses based on component ID
			addresses := []string{
				"localhost",
				"127.0.0.1",
				componentID,
				strings.Split(componentID, "-")[0], // Member ID as hostname
			}

			// Add Docker service names if in container
			if _, err := os.Stat("/.dockerenv"); err == nil {
				// Running in Docker, add service name patterns
				addresses = append(addresses,
					fmt.Sprintf("%s-%s", memberID, componentType),
					fmt.Sprintf("%s_%s", memberID, componentType),
				)
			}

			// Generate certificate
			cert, key, err := certManager.GenerateCertificate(certType, componentID, memberID, addresses, certValidity)
			if err != nil {
				return fmt.Errorf("failed to generate certificate: %w", err)
			}

			// Save certificate and key
			if err := certManager.SaveCertificate(cert, key, certPath, keyPath); err != nil {
				return fmt.Errorf("failed to save certificate: %w", err)
			}

			// Secure the key
			if err := os.Chmod(keyPath, 0600); err != nil {
				return fmt.Errorf("failed to secure certificate key: %w", err)
			}

			fmt.Printf("✓ Certificate generated for %s '%s'\n", componentType, componentID)
		} else {
			logger.Info("Using existing certificate",
				zap.String("component_type", componentType),
				zap.String("component_id", componentID))
			fmt.Printf("✓ Using existing certificate for %s '%s'\n", componentType, componentID)
		}

		// Step 3: Generate configuration snippet
		fmt.Println("\n📋 Configuration:")
		fmt.Printf("export COLLECTIVE_CA_CERT=%s\n", caCertPath)
		fmt.Printf("export COLLECTIVE_CERT=%s\n", certPath)
		fmt.Printf("export COLLECTIVE_KEY=%s\n", keyPath)

		// Generate JSON config snippet
		fmt.Println("\n📄 JSON Configuration:")
		fmt.Printf(`{
  "auth": {
    "enabled": true,
    "ca_cert": "%s",
    "cert": "%s",
    "key": "%s",
    "min_tls_version": "1.3"
  }
}
`, caCertPath, certPath, keyPath)

		return nil
	},
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func init() {
	authAutoInitCmd.Flags().StringP("member-id", "m", "", "Member ID")
	authAutoInitCmd.Flags().StringP("component-type", "t", "", "Component type (coordinator/node/client)")
	authAutoInitCmd.Flags().StringP("component-id", "i", "", "Component ID (auto-generated if not set)")
	authAutoInitCmd.Flags().String("cert-dir", "/collective/certs", "Certificate directory")
	authAutoInitCmd.Flags().Duration("ca-validity", 5*365*24*time.Hour, "CA validity period")
	authAutoInitCmd.Flags().Duration("cert-validity", 90*24*time.Hour, "Certificate validity period")
	authAutoInitCmd.Flags().BoolP("force", "f", false, "Force regeneration of existing certificates")
}
