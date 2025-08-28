package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"collective/pkg/auth"
	"collective/pkg/config"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Certificate and authentication management",
	Long:  "Manage certificates, keys, and authentication for the collective storage system",
}

var authInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new Certificate Authority",
	Long:  "Create a new Certificate Authority for a member with Ed25519 keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		memberID, _ := cmd.Flags().GetString("member-id")
		caPath, _ := cmd.Flags().GetString("ca-path")
		validity, _ := cmd.Flags().GetDuration("validity")

		if memberID == "" {
			return fmt.Errorf("member-id is required")
		}

		if caPath == "" {
			caPath = fmt.Sprintf("./data/certs/%s-ca", memberID)
		}

		// Create certificate manager
		certManager, err := auth.NewCertManager(caPath)
		if err != nil {
			return fmt.Errorf("failed to create certificate manager: %w", err)
		}

		// Generate CA
		fmt.Printf("Generating Ed25519 CA for member '%s'...\n", memberID)
		if err := certManager.GenerateCA(memberID, validity); err != nil {
			return fmt.Errorf("failed to generate CA: %w", err)
		}

		fmt.Printf("✓ CA created successfully at: %s\n", caPath)
		fmt.Printf("  - Certificate: %s/ca.crt\n", caPath)
		fmt.Printf("  - Private Key: %s/ca.key (keep secure!)\n", caPath)
		fmt.Printf("  - Valid for: %v\n", validity)

		return nil
	},
}

var authCertCmd = &cobra.Command{
	Use:   "cert [coordinator|node|client]",
	Short: "Generate certificates",
	Long:  "Generate certificates for coordinators, nodes, or clients",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		componentType := args[0]

		memberID, _ := cmd.Flags().GetString("member-id")
		componentID, _ := cmd.Flags().GetString("id")
		caPath, _ := cmd.Flags().GetString("ca-path")
		outPath, _ := cmd.Flags().GetString("out")
		validity, _ := cmd.Flags().GetDuration("validity")
		addresses, _ := cmd.Flags().GetStringSlice("address")

		if memberID == "" {
			return fmt.Errorf("member-id is required")
		}

		if componentID == "" {
			return fmt.Errorf("id is required")
		}

		if caPath == "" {
			caPath = fmt.Sprintf("./data/certs/%s-ca", memberID)
		}

		if outPath == "" {
			outPath = fmt.Sprintf("./data/certs/%s-%s", memberID, componentID)
		}

		// Create certificate manager
		certManager, err := auth.NewCertManager(caPath)
		if err != nil {
			return fmt.Errorf("failed to create certificate manager: %w", err)
		}

		// Map component type
		var compType auth.ComponentType
		switch componentType {
		case "coordinator":
			compType = auth.ComponentCoordinator
			if len(addresses) == 0 {
				addresses = []string{"localhost", "127.0.0.1"}
			}
		case "node":
			compType = auth.ComponentNode
			if len(addresses) == 0 {
				addresses = []string{"localhost", "127.0.0.1"}
			}
		case "client":
			compType = auth.ComponentClient
		default:
			return fmt.Errorf("invalid component type: %s (use coordinator, node, or client)", componentType)
		}

		// Generate certificate
		fmt.Printf("Generating Ed25519 certificate for %s '%s'...\n", componentType, componentID)
		cert, key, err := certManager.GenerateCertificate(compType, componentID, memberID, addresses, validity)
		if err != nil {
			return fmt.Errorf("failed to generate certificate: %w", err)
		}

		// Create output directory
		if err := os.MkdirAll(outPath, 0700); err != nil {
			return fmt.Errorf("failed to create output directory: %w", err)
		}

		// Save certificate and key
		certPath := filepath.Join(outPath, "cert.pem")
		keyPath := filepath.Join(outPath, "key.pem")

		if err := certManager.SaveCertificate(cert, key, certPath, keyPath); err != nil {
			return fmt.Errorf("failed to save certificate: %w", err)
		}

		fmt.Printf("✓ Certificate generated successfully at: %s\n", outPath)
		fmt.Printf("  - Certificate: %s\n", certPath)
		fmt.Printf("  - Private Key: %s (keep secure!)\n", keyPath)
		fmt.Printf("  - Component: %s\n", componentType)
		fmt.Printf("  - ID: %s\n", componentID)
		fmt.Printf("  - Member: %s\n", memberID)
		if len(addresses) > 0 {
			fmt.Printf("  - Addresses: %v\n", addresses)
		}
		fmt.Printf("  - Valid until: %v\n", cert.NotAfter)

		return nil
	},
}

var authVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify a certificate",
	Long:  "Verify a certificate against a CA",
	RunE: func(cmd *cobra.Command, args []string) error {
		certPath, _ := cmd.Flags().GetString("cert")
		caPath, _ := cmd.Flags().GetString("ca")

		if certPath == "" {
			return fmt.Errorf("cert path is required")
		}

		// Create certificate manager
		certManager, err := auth.NewCertManager("")
		if err != nil {
			return fmt.Errorf("failed to create certificate manager: %w", err)
		}

		// Load certificate
		cert, err := certManager.LoadCertificate(certPath)
		if err != nil {
			return fmt.Errorf("failed to load certificate: %w", err)
		}

		// Verify against CA if provided
		if caPath != "" {
			// Load CA certificate
			caCert, err := certManager.LoadCertificate(caPath)
			if err != nil {
				return fmt.Errorf("failed to load CA certificate: %w", err)
			}

			// Create certificate pool with CA
			roots := x509.NewCertPool()
			roots.AddCert(caCert)

			// Verify certificate
			opts := x509.VerifyOptions{
				Roots:     roots,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			}

			if _, err := cert.Verify(opts); err != nil {
				fmt.Printf("✗ Certificate verification FAILED: %v\n", err)
				return nil
			}

			fmt.Printf("✓ Certificate verified successfully against CA: %s\n", caCert.Subject.CommonName)
		}

		// Display certificate information
		fmt.Printf("\nCertificate Information:\n")
		fmt.Printf("  - Subject: %s\n", cert.Subject.CommonName)
		fmt.Printf("  - Issuer: %s\n", cert.Issuer.CommonName)
		fmt.Printf("  - Serial: %s\n", cert.SerialNumber)
		fmt.Printf("  - Not Before: %v\n", cert.NotBefore)
		fmt.Printf("  - Not After: %v\n", cert.NotAfter)

		// Check expiry
		now := time.Now()
		if now.Before(cert.NotBefore) {
			fmt.Printf("  - Status: NOT YET VALID\n")
		} else if now.After(cert.NotAfter) {
			fmt.Printf("  - Status: EXPIRED\n")
		} else {
			timeLeft := cert.NotAfter.Sub(now)
			fmt.Printf("  - Status: VALID (expires in %v)\n", timeLeft.Round(time.Hour))
		}

		// Display SANs
		if len(cert.DNSNames) > 0 {
			fmt.Printf("  - DNS Names: %v\n", cert.DNSNames)
		}
		if len(cert.IPAddresses) > 0 {
			fmt.Printf("  - IP Addresses: %v\n", cert.IPAddresses)
		}

		// Extract custom extensions if present
		identity, err := certManager.GetIdentityFromCert(cert)
		if err == nil {
			fmt.Printf("  - Component Type: %s\n", identity.Type)
			fmt.Printf("  - Member ID: %s\n", identity.MemberID)
			fmt.Printf("  - Component ID: %s\n", identity.ComponentID)
		}

		return nil
	},
}

var authExportCmd = &cobra.Command{
	Use:   "export-ca",
	Short: "Export CA certificate",
	Long:  "Export the CA certificate for distribution to other components",
	RunE: func(cmd *cobra.Command, args []string) error {
		caPath, _ := cmd.Flags().GetString("ca-path")
		outFile, _ := cmd.Flags().GetString("out")

		if caPath == "" {
			return fmt.Errorf("ca-path is required")
		}

		if outFile == "" {
			outFile = "ca-cert.pem"
		}

		// Read CA certificate
		srcPath := filepath.Join(caPath, "ca.crt")
		certData, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("failed to read CA certificate: %w", err)
		}

		// Write to output file
		if err := os.WriteFile(outFile, certData, 0644); err != nil {
			return fmt.Errorf("failed to write CA certificate: %w", err)
		}

		fmt.Printf("✓ CA certificate exported to: %s\n", outFile)
		fmt.Printf("  Distribute this file to components that need to verify certificates from this CA\n")

		return nil
	},
}

var authInfoCmd = &cobra.Command{
	Use:   "info [cert-file]",
	Short: "Display certificate information",
	Long:  "Display detailed information about a certificate file including expiry, subject, and issuer",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var certPath string
		useContext, _ := cmd.Flags().GetBool("context")

		if useContext {
			// Use current context's certificates
			cfg, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			if cfg.Defaults.PreferredCollective == "" {
				return fmt.Errorf("no preferred collective set - use 'collective federated set-default'")
			}

			collective, err := cfg.GetCollectiveByID(cfg.Defaults.PreferredCollective)
			if err != nil {
				return fmt.Errorf("failed to get preferred collective: %w", err)
			}

			fmt.Printf("=== Authentication Information for Collective: %s ===\n\n", collective.CollectiveID)

			// Display CA certificate info
			if collective.Certificates.CACert != "" {
				fmt.Println("CA CERTIFICATE:")
				fmt.Printf("Path: %s\n", collective.Certificates.CACert)
				if info, err := auth.LoadCertificateInfo(collective.Certificates.CACert); err == nil {
					displayCertInfo(info, "  ")
				} else {
					fmt.Printf("  Error: %v\n", err)
				}
				fmt.Println()
			}

			// Display client certificate info
			if collective.Certificates.ClientCert != "" {
				fmt.Println("CLIENT CERTIFICATE:")
				fmt.Printf("Path: %s\n", collective.Certificates.ClientCert)
				if info, err := auth.LoadCertificateInfo(collective.Certificates.ClientCert); err == nil {
					displayCertInfo(info, "  ")

					// Check expiry warning
					if info.ExpiresIn < 30*24*time.Hour && !info.IsExpired {
						fmt.Printf("\n  ⚠️  WARNING: Certificate expires in %d days!\n",
							int(info.ExpiresIn.Hours()/24))
					}
				} else {
					fmt.Printf("  Error: %v\n", err)
				}
				fmt.Println()
			}

			// Verify chain
			if collective.Certificates.ClientCert != "" && collective.Certificates.CACert != "" {
				fmt.Println("CERTIFICATE CHAIN VERIFICATION:")
				if err := auth.VerifyCertificateChain(collective.Certificates.ClientCert, collective.Certificates.CACert); err == nil {
					fmt.Println("  ✅ Certificate chain is valid")
				} else {
					fmt.Printf("  ❌ Verification failed: %v\n", err)
				}
			}

			return nil
		}

		// Single certificate file
		if len(args) == 0 {
			return fmt.Errorf("certificate file path required (or use --context flag)")
		}
		certPath = args[0]

		info, err := auth.LoadCertificateInfo(certPath)
		if err != nil {
			return fmt.Errorf("failed to load certificate: %w", err)
		}

		fmt.Printf("=== Certificate Information ===\n")
		fmt.Printf("File: %s\n\n", certPath)
		displayCertInfo(info, "")

		return nil
	},
}

func displayCertInfo(info *auth.CertificateInfo, indent string) {
	// Status
	if info.IsExpired {
		fmt.Printf("%sStatus: ❌ EXPIRED\n", indent)
	} else if info.ExpiresIn < 30*24*time.Hour {
		fmt.Printf("%sStatus: ⚠️  EXPIRING SOON (%d days)\n", indent, int(info.ExpiresIn.Hours()/24))
	} else {
		fmt.Printf("%sStatus: ✅ Valid\n", indent)
	}

	// Basic info
	fmt.Printf("%sType: %s\n", indent, info.ComponentType)
	if info.IsCA {
		fmt.Printf("%sCA Certificate: Yes\n", indent)
	}

	// Subject/Issuer
	fmt.Printf("%sSubject: %s\n", indent, info.Subject)
	fmt.Printf("%sIssuer: %s\n", indent, info.Issuer)
	fmt.Printf("%sSerial: %s\n", indent, info.SerialNumber)

	// Validity
	fmt.Printf("%sValid From: %s\n", indent, info.NotBefore.Format("2006-01-02 15:04:05 MST"))
	fmt.Printf("%sValid Until: %s\n", indent, info.NotAfter.Format("2006-01-02 15:04:05 MST"))

	if !info.IsExpired {
		days := int(info.ExpiresIn.Hours() / 24)
		if days > 0 {
			fmt.Printf("%sExpires In: %d days\n", indent, days)
		} else {
			hours := int(info.ExpiresIn.Hours())
			fmt.Printf("%sExpires In: %d hours\n", indent, hours)
		}
	}

	// DNS Names
	if len(info.DNSNames) > 0 {
		fmt.Printf("%sDNS Names: %v\n", indent, info.DNSNames)
	}

	// Key usage
	if info.KeyUsage != "" && info.KeyUsage != "None" {
		fmt.Printf("%sKey Usage: %s\n", indent, info.KeyUsage)
	}

	// Extended key usage
	if len(info.ExtKeyUsage) > 0 {
		fmt.Printf("%sExt Key Usage: %v\n", indent, info.ExtKeyUsage)
	}
}

func authCommand() *cobra.Command {
	// auth init flags
	authInitCmd.Flags().StringP("member-id", "m", "", "Member ID for the CA")
	authInitCmd.Flags().String("ca-path", "", "Path to store CA files (default: ./data/certs/<member-id>-ca)")
	authInitCmd.Flags().Duration("validity", 5*365*24*time.Hour, "CA validity period")

	// auth cert flags
	authCertCmd.Flags().StringP("member-id", "m", "", "Member ID")
	authCertCmd.Flags().String("id", "", "Component ID (node ID, coordinator ID, or client ID)")
	authCertCmd.Flags().String("ca-path", "", "Path to CA files")
	authCertCmd.Flags().StringP("out", "o", "", "Output path for certificate files")
	authCertCmd.Flags().Duration("validity", 90*24*time.Hour, "Certificate validity period")
	authCertCmd.Flags().StringSlice("address", []string{}, "Addresses to include in certificate (for coordinators/nodes)")

	// auth verify flags
	authVerifyCmd.Flags().String("cert", "", "Path to certificate to verify")
	authVerifyCmd.Flags().String("ca", "", "Path to CA certificate")

	// auth export-ca flags
	authExportCmd.Flags().String("ca-path", "", "Path to CA files")
	authExportCmd.Flags().StringP("out", "o", "ca-cert.pem", "Output file for CA certificate")

	// auth info flags
	authInfoCmd.Flags().BoolP("context", "c", false, "Show certificates from current context")

	// Add subcommands
	authCmd.AddCommand(authInitCmd)
	authCmd.AddCommand(authCertCmd)
	authCmd.AddCommand(authVerifyCmd)
	authCmd.AddCommand(authExportCmd)
	authCmd.AddCommand(authAutoInitCmd)
	authCmd.AddCommand(authInfoCmd)
	
	// Add federation CA commands
	AddFederationCACommands(authCmd)

	return authCmd
}
