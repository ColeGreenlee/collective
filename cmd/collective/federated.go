package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"collective/pkg/config"

	"github.com/spf13/cobra"
)

// identityCmd returns the identity management command
func identityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Manage your identity and collective connections",
		Long: `Manage your identity across multiple collectives.
		
Your identity allows you to seamlessly connect to multiple collectives
with automatic certificate resolution based on the coordinator address.`,
	}

	cmd.AddCommand(
		identityInitCmd(),
		identityAddCmd(),
		identityListCmd(),
		identityRemoveCmd(),
		identitySetDefaultCmd(),
		identityStatusCmd(),
	)

	return cmd
}

// identityInitCmd initializes a new identity
func identityInitCmd() *cobra.Command {
	var (
		globalID    string
		displayName string
		force       bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new identity",
		Long: `Initialize a new identity with a global ID and display name.
		
This creates a new Ed25519 key pair and sets up your identity configuration.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if federated identity already exists
			clientConfig, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load client config: %w", err)
			}

			if clientConfig.UserIdentity != nil && clientConfig.UserIdentity.GlobalID != "" && !force {
				return fmt.Errorf("federated identity already exists: %s. Use --force to overwrite",
					clientConfig.UserIdentity.GlobalID)
			}

			// Prompt for values if not provided
			if globalID == "" {
				fmt.Print("Enter global ID (e.g., alice@collective.network): ")
				fmt.Scanln(&globalID)
			}

			if displayName == "" {
				fmt.Print("Enter display name (e.g., Alice Johnson): ")
				fmt.Scanln(&displayName)
			}

			// Create user identity
			identity, privateKey, err := config.CreateUserIdentity(globalID, displayName)
			if err != nil {
				return fmt.Errorf("failed to create user identity: %w", err)
			}

			// Save private key
			configDir := config.GetConfigDir()
			if err := os.MkdirAll(configDir, 0700); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			privateKeyPath := filepath.Join(configDir, "identity.key")
			privateKeyHex := hex.EncodeToString(privateKey)
			if err := os.WriteFile(privateKeyPath, []byte(privateKeyHex), 0600); err != nil {
				return fmt.Errorf("failed to save private key: %w", err)
			}

			// Update client config with user identity
			clientConfig.UserIdentity = identity
			if err := clientConfig.Save(); err != nil {
				return fmt.Errorf("failed to save client config: %w", err)
			}

			fmt.Printf("✅ Federated identity created successfully!\n")
			fmt.Printf("   Global ID: %s\n", identity.GlobalID)
			fmt.Printf("   Display Name: %s\n", identity.DisplayName)
			fmt.Printf("   Public Key: %s\n", identity.PublicKey)
			fmt.Printf("   Private Key: %s (keep this secure!)\n", privateKeyPath)

			return nil
		},
	}

	cmd.Flags().StringVar(&globalID, "global-id", "", "Global federated identity ID")
	cmd.Flags().StringVar(&displayName, "display-name", "", "Display name for the identity")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing identity")

	return cmd
}

// identityAddCmd adds a collective to the federated configuration
func identityAddCmd() *cobra.Command {
	var (
		collectiveID string
		coordinator  string
		memberID     string
		role         string
		caCert       string
		clientCert   string
		clientKey    string
		trustLevel   string
		description  string
		autoDiscover bool
		setDefault   bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a collective to your identity",
		Long: `Add a collective to your identity configuration.
		
This allows automatic certificate resolution when connecting to the collective.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Prompt for required values if not provided
			if collectiveID == "" {
				fmt.Print("Enter collective ID (e.g., home.alice): ")
				fmt.Scanln(&collectiveID)
			}

			if coordinator == "" {
				fmt.Print("Enter coordinator address (e.g., alice.collective:8001): ")
				fmt.Scanln(&coordinator)
			}

			if memberID == "" {
				fmt.Print("Enter member ID in this collective: ")
				fmt.Scanln(&memberID)
			}

			// Load client config
			clientConfig, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load client config: %w", err)
			}

			// Set up certificate paths in collective-specific directory
			certDir := config.GetCollectiveCertificateDir(collectiveID)
			if err := os.MkdirAll(certDir, 0700); err != nil {
				return fmt.Errorf("failed to create certificate directory: %w", err)
			}

			caCertPath := filepath.Join(certDir, "ca.crt")
			clientCertPath := filepath.Join(certDir, "client.crt")
			clientKeyPath := filepath.Join(certDir, "client.key")

			// Copy certificates if provided
			if caCert != "" {
				if err := copyFile(caCert, caCertPath); err != nil {
					return fmt.Errorf("failed to copy CA certificate: %w", err)
				}
			}
			if clientCert != "" {
				if err := copyFile(clientCert, clientCertPath); err != nil {
					return fmt.Errorf("failed to copy client certificate: %w", err)
				}
			}
			if clientKey != "" {
				if err := copyFile(clientKey, clientKeyPath); err != nil {
					return fmt.Errorf("failed to copy client key: %w", err)
				}
			}

			// Set defaults
			if role == "" {
				role = "member"
			}
			if trustLevel == "" {
				trustLevel = "medium"
			}

			// Create collective info
			collective := config.CollectiveInfo{
				CollectiveID:       collectiveID,
				CoordinatorAddress: coordinator,
				MemberID:           memberID,
				Role:               role,
				Certificates: config.CertificateConfig{
					CACert:     caCertPath,
					ClientCert: clientCertPath,
					ClientKey:  clientKeyPath,
				},
				AutoDiscover: autoDiscover,
				TrustLevel:   trustLevel,
				Description:  description,
			}

			// Add to configuration
			if err := clientConfig.AddCollective(collective); err != nil {
				return fmt.Errorf("failed to add collective: %w", err)
			}

			// Set as default if requested
			if setDefault {
				clientConfig.Defaults.PreferredCollective = collectiveID
				if err := clientConfig.Save(); err != nil {
					return fmt.Errorf("failed to save config: %w", err)
				}
			}

			fmt.Printf("✅ Collective %s added successfully!\n", collectiveID)
			fmt.Printf("   Coordinator: %s\n", coordinator)
			fmt.Printf("   Member ID: %s\n", memberID)
			fmt.Printf("   Role: %s\n", role)
			fmt.Printf("   Certificates: %s\n", certDir)
			if setDefault {
				fmt.Printf("   Set as default collective\n")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&collectiveID, "collective-id", "", "Collective identifier")
	cmd.Flags().StringVar(&coordinator, "coordinator", "", "Coordinator address")
	cmd.Flags().StringVar(&memberID, "member-id", "", "Member ID in the collective")
	cmd.Flags().StringVar(&role, "role", "member", "Role in the collective (owner, member, guest)")
	cmd.Flags().StringVar(&caCert, "ca-cert", "", "Path to CA certificate file")
	cmd.Flags().StringVar(&clientCert, "client-cert", "", "Path to client certificate file")
	cmd.Flags().StringVar(&clientKey, "client-key", "", "Path to client private key file")
	cmd.Flags().StringVar(&trustLevel, "trust-level", "medium", "Trust level (high, medium, low)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the collective")
	cmd.Flags().BoolVar(&autoDiscover, "auto-discover", true, "Enable auto-discovery for this collective")
	cmd.Flags().BoolVar(&setDefault, "set-default", false, "Set as the default collective")

	return cmd
}

// identityListCmd lists all configured collectives
func identityListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured collectives",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientConfig, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load client config: %w", err)
			}

			if clientConfig.UserIdentity == nil || clientConfig.UserIdentity.GlobalID == "" {
				fmt.Println("No identity configured. Run 'collective identity init' first.")
				return nil
			}

			// Display user identity
			fmt.Printf("🌐 Identity: %s (%s)\n",
				clientConfig.UserIdentity.GlobalID,
				clientConfig.UserIdentity.DisplayName)
			fmt.Printf("   Public Key: %s\n", clientConfig.UserIdentity.PublicKey)
			fmt.Println()

			if len(clientConfig.Collectives) == 0 {
				fmt.Println("No collectives configured. Run 'collective identity add' to add one.")
				return nil
			}

			// Display collectives in table format
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "COLLECTIVE\tCOORDINATOR\tMEMBER ID\tROLE\tTRUST\tSTATUS")
			fmt.Fprintln(w, "----------\t-----------\t---------\t----\t-----\t------")

			for _, collective := range clientConfig.Collectives {
				status := "🟢"
				if !collective.AutoDiscover {
					status = "⚪"
				}
				if collective.CollectiveID == clientConfig.Defaults.PreferredCollective {
					status += " (default)"
				}

				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					collective.CollectiveID,
					collective.CoordinatorAddress,
					collective.MemberID,
					collective.Role,
					collective.TrustLevel,
					status)
			}

			w.Flush()
			return nil
		},
	}

	return cmd
}

// identityRemoveCmd removes a collective from the federated configuration
func identityRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove [collective-id]",
		Short: "Remove a collective from federated configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			collectiveID := args[0]

			clientConfig, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load client config: %w", err)
			}

			// Remove collective
			if err := clientConfig.RemoveCollective(collectiveID); err != nil {
				return fmt.Errorf("failed to remove collective: %w", err)
			}

			fmt.Printf("✅ Collective %s removed successfully\n", collectiveID)
			return nil
		},
	}

	return cmd
}

// identitySetDefaultCmd sets the default collective
func identitySetDefaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-default [collective-id]",
		Short: "Set the default collective",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			collectiveID := args[0]

			clientConfig, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load client config: %w", err)
			}

			// Verify collective exists
			if _, err := clientConfig.GetCollectiveByID(collectiveID); err != nil {
				return fmt.Errorf("collective not found: %w", err)
			}

			// Set as default
			clientConfig.Defaults.PreferredCollective = collectiveID
			if err := clientConfig.Save(); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("✅ Default collective set to %s\n", collectiveID)
			return nil
		},
	}

	return cmd
}

// identityStatusCmd shows the status of the federated identity system
func identityStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show federated identity status",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientConfig, err := config.LoadClientConfig()
			if err != nil {
				return fmt.Errorf("failed to load client config: %w", err)
			}

			if clientConfig.UserIdentity == nil || clientConfig.UserIdentity.GlobalID == "" {
				fmt.Println("❌ No identity configured")
				fmt.Println("   Run 'collective identity init' to get started")
				return nil
			}

			fmt.Printf("🌐 Identity Status\n")
			fmt.Printf("   Global ID: %s\n", clientConfig.UserIdentity.GlobalID)
			fmt.Printf("   Display Name: %s\n", clientConfig.UserIdentity.DisplayName)
			fmt.Printf("   Created: %s\n", clientConfig.UserIdentity.Created.Format("2006-01-02 15:04:05"))
			fmt.Printf("   Public Key: %s\n", clientConfig.UserIdentity.PublicKey)
			fmt.Println()

			fmt.Printf("📊 Collectives: %d configured\n", len(clientConfig.Collectives))
			if clientConfig.Defaults.PreferredCollective != "" {
				fmt.Printf("   Default: %s\n", clientConfig.Defaults.PreferredCollective)
			}
			fmt.Printf("   Auto-discovery: %t\n", clientConfig.Defaults.AutoDiscovery)
			fmt.Println()

			// Test connection resolution
			if len(clientConfig.Collectives) > 0 {
				fmt.Println("🔗 Connection Resolution Test:")
				for _, collective := range clientConfig.Collectives {
					connectionConfig, err := clientConfig.ResolveConnection(collective.CoordinatorAddress)
					if err != nil {
						fmt.Printf("   ❌ %s: %v\n", collective.CollectiveID, err)
					} else {
						fmt.Printf("   ✅ %s: %s\n", collective.CollectiveID, connectionConfig.Coordinator)
					}
				}
			}

			return nil
		},
	}

	return cmd
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
