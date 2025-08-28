package main

import (
	"fmt"
	"net"
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
		federationDir := "/collective/federation"

		if err := os.MkdirAll(caDir, 0755); err != nil {
			return fmt.Errorf("failed to create CA directory: %w", err)
		}
		if err := os.MkdirAll(componentDir, 0755); err != nil {
			return fmt.Errorf("failed to create component directory: %w", err)
		}
		if err := os.MkdirAll(federationDir, 0755); err != nil {
			return fmt.Errorf("failed to create federation directory: %w", err)
		}

		// Step 1: Check/Create Federation CA hierarchy
		federationRootCert := filepath.Join(federationDir, "federation-root-ca.crt")
		federationRootKey := filepath.Join(federationDir, "federation-root-ca.key")
		caCertPath := filepath.Join(caDir, fmt.Sprintf("%s-ca.crt", memberID))
		caKeyPath := filepath.Join(caDir, fmt.Sprintf("%s-ca.key", memberID))

		// Check if federation root CA exists
		federationRootExists := fileExists(federationRootCert) && fileExists(federationRootKey)

		// Get federation domain from environment
		federationDomain := os.Getenv("COLLECTIVE_FEDERATION_DOMAIN")
		if federationDomain == "" {
			federationDomain = "homelab.collective"
		}

		if !federationRootExists {
			logger.Info("Creating federation root CA", zap.String("federation_domain", federationDomain))

			// Create federation certificate manager with a temporary directory
			federationCADir := filepath.Join(caDir, "federation-root")
			os.MkdirAll(federationCADir, 0755)

			fedCertManager := auth.NewFederationCertManager(federationCADir)

			// Generate federation root CA
			if err := fedCertManager.GenerateFederationRootCA(federationDomain, caValidity); err != nil {
				return fmt.Errorf("failed to generate federation root CA: %w", err)
			}

			// Copy the generated CA files to expected locations (can't move across volumes)
			caCertData, err := os.ReadFile(filepath.Join(federationCADir, "ca.crt"))
			if err != nil {
				return fmt.Errorf("failed to read generated federation root CA certificate: %w", err)
			}
			caKeyData, err := os.ReadFile(filepath.Join(federationCADir, "ca.key"))
			if err != nil {
				return fmt.Errorf("failed to read generated federation root CA key: %w", err)
			}

			if err := os.WriteFile(federationRootCert, caCertData, 0644); err != nil {
				return fmt.Errorf("failed to copy federation root CA certificate: %w", err)
			}
			if err := os.WriteFile(federationRootKey, caKeyData, 0600); err != nil {
				return fmt.Errorf("failed to copy federation root CA key: %w", err)
			}
			os.RemoveAll(federationCADir) // Clean up temp directory

			fmt.Printf("✓ Federation root CA generated for '%s'\n", federationDomain)
		}

		// Check if member intermediate CA exists
		caExists := fileExists(caCertPath) && fileExists(caKeyPath)

		if !caExists || force {
			logger.Info("Generating member intermediate CA", zap.String("member_id", memberID))

			// Create temporary directory for loading federation root CA
			tempFedCADir := filepath.Join(caDir, "temp-federation-root")
			os.MkdirAll(tempFedCADir, 0755)
			defer os.RemoveAll(tempFedCADir)

			// Copy federation root CA files to temp directory with expected names
			fedRootData, err := os.ReadFile(federationRootCert)
			if err != nil {
				return fmt.Errorf("failed to read federation root CA certificate: %w", err)
			}
			fedRootKeyData, err := os.ReadFile(federationRootKey)
			if err != nil {
				return fmt.Errorf("failed to read federation root CA key: %w", err)
			}

			if err := os.WriteFile(filepath.Join(tempFedCADir, "ca.crt"), fedRootData, 0644); err != nil {
				return fmt.Errorf("failed to copy federation root CA certificate: %w", err)
			}
			if err := os.WriteFile(filepath.Join(tempFedCADir, "ca.key"), fedRootKeyData, 0600); err != nil {
				return fmt.Errorf("failed to copy federation root CA key: %w", err)
			}

			// Load federation root CA
			fedCertManager := auth.NewFederationCertManager(tempFedCADir)
			if err := fedCertManager.LoadCA(); err != nil {
				return fmt.Errorf("failed to load federation root CA: %w", err)
			}

			// Generate member intermediate CA using federation hierarchy
			memberDomain := fmt.Sprintf("%s.%s", memberID, federationDomain)
			if err := fedCertManager.GenerateMemberCA(memberDomain, caValidity, caCertPath); err != nil {
				return fmt.Errorf("failed to generate member intermediate CA: %w", err)
			}

			// Copy member CA to shared federation directory for peer access
			federationMemberCA := filepath.Join(federationDir, fmt.Sprintf("%s-ca.crt", memberID))
			if caCertData, err := os.ReadFile(caCertPath); err == nil {
				if err := os.WriteFile(federationMemberCA, caCertData, 0644); err != nil {
					logger.Warn("Failed to copy member CA to federation directory",
						zap.String("member_id", memberID), zap.Error(err))
				} else {
					logger.Info("Member CA copied to federation directory for peer access",
						zap.String("member_id", memberID),
						zap.String("path", federationMemberCA))
				}
			}

			fmt.Printf("✓ Member intermediate CA generated for '%s'\n", memberDomain)
		} else {
			logger.Info("Using existing member intermediate CA", zap.String("member_id", memberID))

			// Even for existing CAs, ensure they are copied to federation directory
			federationMemberCA := filepath.Join(federationDir, fmt.Sprintf("%s-ca.crt", memberID))
			if !fileExists(federationMemberCA) {
				if caCertData, err := os.ReadFile(caCertPath); err == nil {
					if err := os.WriteFile(federationMemberCA, caCertData, 0644); err != nil {
						logger.Warn("Failed to copy existing member CA to federation directory",
							zap.String("member_id", memberID), zap.Error(err))
					} else {
						logger.Info("Existing member CA copied to federation directory for peer access",
							zap.String("member_id", memberID),
							zap.String("path", federationMemberCA))
					}
				}
			}

			fmt.Printf("✓ Using existing member intermediate CA for member '%s'\n", memberID)
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

			// Copy member intermediate CA files to temp directory (cert manager expects ca.crt/ca.key structure)
			caCertData, err := os.ReadFile(caCertPath)
			if err != nil {
				return fmt.Errorf("failed to read member intermediate CA certificate: %w", err)
			}
			caKeyData, err := os.ReadFile(caKeyPath)
			if err != nil {
				return fmt.Errorf("failed to read member intermediate CA key: %w", err)
			}

			if err := os.WriteFile(filepath.Join(tempCADir, "ca.crt"), caCertData, 0644); err != nil {
				return fmt.Errorf("failed to copy member intermediate CA certificate: %w", err)
			}
			if err := os.WriteFile(filepath.Join(tempCADir, "ca.key"), caKeyData, 0600); err != nil {
				return fmt.Errorf("failed to copy member intermediate CA key: %w", err)
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

				// Try to get container's IP address
				if hostIP := getContainerIP(); hostIP != "" {
					addresses = append(addresses, hostIP)
				}
			}

			// Add additional addresses from environment variable
			if extraAddrs := os.Getenv("COLLECTIVE_CERT_ADDRESSES"); extraAddrs != "" {
				for _, addr := range strings.Split(extraAddrs, ",") {
					addr = strings.TrimSpace(addr)
					if addr != "" {
						addresses = append(addresses, addr)
					}
				}
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

		// Step 3: Ensure federation root CA is available for peer validation
		fedRootCACopy := filepath.Join(caDir, "federation-root-ca.crt")
		if !fileExists(fedRootCACopy) {
			if fileExists(federationRootCert) {
				fedRootData, err := os.ReadFile(federationRootCert)
				if err != nil {
					logger.Warn("Failed to read federation root CA for copying", zap.Error(err))
				} else {
					if err := os.WriteFile(fedRootCACopy, fedRootData, 0644); err != nil {
						logger.Warn("Failed to copy federation root CA to ca directory", zap.Error(err))
					} else {
						fmt.Printf("✓ Federation root CA copied for peer validation\n")
					}
				}
			}
		}

		// Step 4: Copy all member CA certificates for peer trust
		logger.Info("Copying peer member CA certificates for federation trust")

		// Look for member CA certificates in the shared federation directory
		if entries, err := os.ReadDir(federationDir); err == nil {
			copiedCount := 0
			for _, entry := range entries {
				// Copy member CA certificates (format: memberId-ca.crt)
				if strings.HasSuffix(entry.Name(), "-ca.crt") && entry.Name() != fmt.Sprintf("%s-ca.crt", memberID) {
					sourcePath := filepath.Join(federationDir, entry.Name())
					destPath := filepath.Join(caDir, entry.Name())

					if !fileExists(destPath) {
						if certData, err := os.ReadFile(sourcePath); err == nil {
							if err := os.WriteFile(destPath, certData, 0644); err == nil {
								copiedCount++
								logger.Debug("Copied peer member CA certificate",
									zap.String("cert", entry.Name()),
									zap.String("from", sourcePath),
									zap.String("to", destPath))
							} else {
								logger.Warn("Failed to copy peer member CA certificate",
									zap.String("cert", entry.Name()),
									zap.Error(err))
							}
						}
					}
				}
			}
			if copiedCount > 0 {
				fmt.Printf("✓ Copied %d peer member CA certificates for federation trust\n", copiedCount)
			}
		} else {
			logger.Warn("Failed to read federation directory for peer CAs", zap.Error(err))
		}

		// Step 5: Generate configuration snippet
		fmt.Println("\n📋 Configuration:")
		fmt.Printf("export COLLECTIVE_CA_CERT=%s\n", caCertPath)
		fmt.Printf("export COLLECTIVE_FEDERATION_ROOT_CA=%s\n", fedRootCACopy)
		fmt.Printf("export COLLECTIVE_CERT=%s\n", certPath)
		fmt.Printf("export COLLECTIVE_KEY=%s\n", keyPath)

		// Generate JSON config snippet
		fmt.Println("\n📄 JSON Configuration:")
		fmt.Printf(`{
  "auth": {
    "enabled": true,
    "ca_cert": "%s",
    "federation_root_ca": "%s",
    "cert": "%s",
    "key": "%s",
    "min_tls_version": "1.3"
  }
}
`, caCertPath, fedRootCACopy, certPath, keyPath)

		return nil
	},
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// getContainerIP returns the container's IP address on the Docker network
func getContainerIP() string {
	// Get all network interfaces
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			ip := ipNet.IP
			// Return first non-loopback IPv4 address
			if ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}

	return ""
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
