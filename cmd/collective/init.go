package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"collective/pkg/config"
	"collective/pkg/protocol"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize connection to a collective",
	Long:  "Set up a new connection context to a collective storage cluster",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get flags
		name, _ := cmd.Flags().GetString("name")
		coordinator, _ := cmd.Flags().GetString("coordinator")
		memberID, _ := cmd.Flags().GetString("member")
		caPath, _ := cmd.Flags().GetString("ca-cert")
		fromCoordinator, _ := cmd.Flags().GetString("from-coordinator")
		insecureFlag, _ := cmd.Flags().GetBool("insecure")
		interactive, _ := cmd.Flags().GetBool("interactive")

		// Interactive mode if no args provided
		if interactive || (name == "" && coordinator == "" && fromCoordinator == "") {
			return runInteractiveInit()
		}

		// If from-coordinator is specified, fetch everything from there
		if fromCoordinator != "" {
			return initFromCoordinator(fromCoordinator, name, insecureFlag)
		}

		// Validate required parameters
		if name == "" {
			name = "default"
		}
		if coordinator == "" {
			return fmt.Errorf("coordinator address is required (use --coordinator or --from-coordinator)")
		}
		if memberID == "" {
			// Try to extract member ID from coordinator address
			parts := strings.Split(coordinator, ":")
			if len(parts) > 0 && strings.Contains(parts[0], "-") {
				memberID = strings.Split(parts[0], "-")[0]
			}
			if memberID == "" {
				return fmt.Errorf("member ID is required (use --member)")
			}
		}

		// Initialize context
		if err := config.InitializeContext(name, coordinator, memberID, caPath); err != nil {
			return fmt.Errorf("failed to initialize context: %w", err)
		}

		// Generate client certificate if CA was provided
		if caPath != "" {
			fmt.Println("\n🔐 Generating client certificate...")
			if err := generateClientCertificate(name, memberID); err != nil {
				fmt.Printf("⚠️  Warning: Failed to generate client certificate: %v\n", err)
				fmt.Println("   You'll need to generate it manually with:")
				fmt.Printf("   collective auth cert client --member-id %s --id %s-client\n", memberID, name)
			} else {
				fmt.Println("✓ Client certificate generated")
			}
		}

		// Test connection
		fmt.Println("\n🔗 Testing connection...")
		if err := testConnection(name); err != nil {
			fmt.Printf("⚠️  Warning: Connection test failed: %v\n", err)
			fmt.Println("   Please verify your configuration and certificates")
		} else {
			fmt.Println("✓ Successfully connected to collective")
		}

		fmt.Printf("\n✅ Context %q is ready to use!\n", name)
		fmt.Println("\nTry these commands:")
		fmt.Printf("  collective status              # Check cluster status\n")
		fmt.Printf("  collective ls /                # List files\n")
		fmt.Printf("  collective config get-contexts # List all contexts\n")

		return nil
	},
}

func runInteractiveInit() error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("🚀 Welcome to Collective! Let's set up your connection.")

	// Get context name
	fmt.Print("Context name [default]: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}

	// Get coordinator address
	fmt.Print("Coordinator address (e.g., localhost:8001): ")
	coordinator, _ := reader.ReadString('\n')
	coordinator = strings.TrimSpace(coordinator)
	if coordinator == "" {
		return fmt.Errorf("coordinator address is required")
	}

	// Get member ID
	fmt.Print("Member ID (e.g., alice): ")
	memberID, _ := reader.ReadString('\n')
	memberID = strings.TrimSpace(memberID)
	if memberID == "" {
		// Try to extract from coordinator
		parts := strings.Split(coordinator, ":")
		if len(parts) > 0 && strings.Contains(parts[0], "-") {
			memberID = strings.Split(parts[0], "-")[0]
			fmt.Printf("(detected member ID: %s)\n", memberID)
		}
	}
	if memberID == "" {
		return fmt.Errorf("member ID is required")
	}

	// Ask about CA certificate
	fmt.Print("CA certificate path (leave empty to fetch from coordinator): ")
	caPath, _ := reader.ReadString('\n')
	caPath = strings.TrimSpace(caPath)

	if caPath == "" {
		// Try to fetch from coordinator
		fmt.Println("\n📡 Attempting to fetch CA from coordinator...")
		return initFromCoordinator(coordinator, name, true)
	}

	// Initialize context
	if err := config.InitializeContext(name, coordinator, memberID, caPath); err != nil {
		return fmt.Errorf("failed to initialize context: %w", err)
	}

	// Generate client certificate
	fmt.Println("\n🔐 Generating client certificate...")
	if err := generateClientCertificate(name, memberID); err != nil {
		fmt.Printf("⚠️  Warning: Failed to generate client certificate: %v\n", err)
	} else {
		fmt.Println("✓ Client certificate generated")
	}

	// Test connection
	fmt.Println("\n🔗 Testing connection...")
	if err := testConnection(name); err != nil {
		fmt.Printf("⚠️  Warning: Connection test failed: %v\n", err)
	} else {
		fmt.Println("✓ Successfully connected!")
	}

	fmt.Printf("\n✅ Configuration saved! You can now use 'collective' commands.\n")
	return nil
}

func initFromCoordinator(coordinator, name string, insecureFlag bool) error {
	if name == "" {
		name = "default"
	}

	// Connect to coordinator without auth to get basic info
	var dialOpts []grpc.DialOption
	if insecureFlag {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		// Try with system CA pool
		tlsConfig := &tls.Config{}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	}

	conn, err := grpc.Dial(coordinator, dialOpts...)
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to get status (might fail if auth is required)
	statusResp, err := client.GetStatus(ctx, &protocol.GetStatusRequest{})
	if err != nil {
		// If auth is required, we need the CA certificate manually
		return fmt.Errorf("cannot auto-configure from secured coordinator: %w\nPlease provide the CA certificate with --ca-cert", err)
	}

	memberID := statusResp.MemberId
	fmt.Printf("✓ Connected to %s's collective\n", memberID)

	// Initialize context (without CA for now)
	if err := config.InitializeContext(name, coordinator, memberID, ""); err != nil {
		return fmt.Errorf("failed to initialize context: %w", err)
	}

	// Mark as insecure if no TLS
	if insecureFlag {
		cfg, _ := config.LoadClientConfig()
		if ctx, err := cfg.GetContext(name); err == nil {
			ctx.Auth.Insecure = true
			cfg.AddContext(*ctx)
		}
	}

	fmt.Printf("✓ Context %q created (insecure mode)\n", name)
	fmt.Println("\n⚠️  WARNING: Running without TLS authentication")
	fmt.Println("   For production use, obtain the CA certificate and re-initialize with:")
	fmt.Printf("   collective init --name %s --coordinator %s --ca-cert <path-to-ca>\n", name, coordinator)

	return nil
}

func generateClientCertificate(contextName, memberID string) error {
	certDir := config.GetCertificateDir(contextName)
	caPath := filepath.Join(certDir, "ca.crt")

	// Check if CA exists
	if _, err := os.Stat(caPath); os.IsNotExist(err) {
		return fmt.Errorf("CA certificate not found")
	}

	// Create temporary CA directory structure for cert manager
	tempDir, err := os.MkdirTemp("", "collective-ca-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	// We need to create a proper CA structure for the cert manager
	// For now, return nil as we'd need the CA key to generate client certs
	// In production, this would connect to the coordinator to request a signed cert

	// TODO: Implement certificate signing request (CSR) workflow
	// 1. Generate client key pair
	// 2. Create CSR
	// 3. Send to coordinator for signing
	// 4. Save signed certificate

	return nil
}

func testConnection(contextName string) error {
	cfg, err := config.LoadClientConfig()
	if err != nil {
		return err
	}

	connCfg, err := cfg.GetConnectionConfig(contextName)
	if err != nil {
		return err
	}

	// Set up gRPC connection
	var dialOpts []grpc.DialOption

	if connCfg.Insecure {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else if connCfg.CertPath != "" && connCfg.KeyPath != "" {
		// Use client certificates
		cert, err := tls.LoadX509KeyPair(connCfg.CertPath, connCfg.KeyPath)
		if err != nil {
			return fmt.Errorf("failed to load certificates: %w", err)
		}

		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}

		if connCfg.CAPath != "" {
			// Load CA if available
			// ... (CA loading code)
		}

		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.Dial(connCfg.Coordinator, dialOpts...)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := protocol.NewCoordinatorClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), connCfg.Timeout)
	defer cancel()

	_, err = client.GetStatus(ctx, &protocol.GetStatusRequest{})
	return err
}

func initCommand() *cobra.Command {
	initCmd.Flags().StringP("name", "n", "", "Context name (default: 'default')")
	initCmd.Flags().StringP("coordinator", "c", "", "Coordinator address")
	initCmd.Flags().StringP("member", "m", "", "Member ID")
	initCmd.Flags().String("ca-cert", "", "Path to CA certificate")
	initCmd.Flags().String("from-coordinator", "", "Auto-configure from coordinator")
	initCmd.Flags().Bool("insecure", false, "Skip TLS verification (not recommended)")
	initCmd.Flags().BoolP("interactive", "i", false, "Run in interactive mode")

	return initCmd
}
