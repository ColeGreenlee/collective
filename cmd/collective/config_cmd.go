package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"collective/pkg/auth"
	"collective/pkg/config"
	"collective/pkg/protocol"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage collective client configuration",
	Long:  "Manage contexts and configuration for connecting to collective clusters",
}

var configGetContextsCmd = &cobra.Command{
	Use:   "get-contexts",
	Short: "List all contexts",
	Long:  "Display all configured contexts for connecting to collectives",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if len(cfg.Contexts) == 0 {
			fmt.Println("No contexts configured. Use 'collective init' to add your first context.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tCOORDINATOR\tMEMBER\tCURRENT")

		for _, ctx := range cfg.Contexts {
			current := ""
			if ctx.Name == cfg.CurrentContext {
				current = "*"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				ctx.Name, ctx.Coordinator, ctx.MemberID, current)
		}

		w.Flush()
		return nil
	},
}

var configCurrentContextCmd = &cobra.Command{
	Use:   "current-context",
	Short: "Display the current context",
	Long:  "Show the name of the current context",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if cfg.CurrentContext == "" {
			fmt.Println("No current context set")
			return nil
		}

		fmt.Println(cfg.CurrentContext)
		return nil
	},
}

var configUseContextCmd = &cobra.Command{
	Use:   "use-context [name]",
	Short: "Set the current context",
	Long:  "Switch to a different context for collective operations",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName := args[0]

		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if err := cfg.SetCurrentContext(contextName); err != nil {
			return err
		}

		fmt.Printf("Switched to context %q\n", contextName)
		return nil
	},
}

var configDeleteContextCmd = &cobra.Command{
	Use:   "delete-context [name]",
	Short: "Delete a context",
	Long:  "Remove a context from the configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		contextName := args[0]
		force, _ := cmd.Flags().GetBool("force")

		if !force {
			fmt.Printf("Are you sure you want to delete context %q? (y/N): ", contextName)
			var response string
			fmt.Scanln(&response)
			if !strings.HasPrefix(strings.ToLower(response), "y") {
				fmt.Println("Cancelled")
				return nil
			}
		}

		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		if err := cfg.RemoveContext(contextName); err != nil {
			return err
		}

		// Also remove certificate directory
		certDir := config.GetCertificateDir(contextName)
		if err := os.RemoveAll(certDir); err != nil {
			fmt.Printf("Warning: failed to remove certificate directory: %v\n", err)
		}

		fmt.Printf("Context %q deleted\n", contextName)
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get [key]",
	Short: "Get a configuration value",
	Long:  "Display a specific configuration value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]

		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		switch key {
		case "current-context":
			fmt.Println(cfg.CurrentContext)
		case "timeout":
			fmt.Println(cfg.Defaults.Timeout)
		case "output-format":
			fmt.Println(cfg.Defaults.OutputFormat)
		case "retry-count":
			fmt.Println(cfg.Defaults.RetryCount)
		default:
			return fmt.Errorf("unknown configuration key: %s", key)
		}

		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set [key] [value]",
	Short: "Set a configuration value",
	Long:  "Update a configuration value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value := args[1]

		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		switch key {
		case "current-context":
			if err := cfg.SetCurrentContext(value); err != nil {
				return err
			}
		case "timeout":
			cfg.Defaults.Timeout = value
			if err := cfg.Save(); err != nil {
				return err
			}
		case "output-format":
			if value != "styled" && value != "json" && value != "plain" {
				return fmt.Errorf("invalid output format: %s (use styled, json, or plain)", value)
			}
			cfg.Defaults.OutputFormat = value
			if err := cfg.Save(); err != nil {
				return err
			}
		case "retry-count":
			var count int
			if _, err := fmt.Sscanf(value, "%d", &count); err != nil {
				return fmt.Errorf("invalid retry count: %s", value)
			}
			cfg.Defaults.RetryCount = count
			if err := cfg.Save(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown configuration key: %s", key)
		}

		fmt.Printf("Configuration updated: %s = %s\n", key, value)
		return nil
	},
}

var configViewCmd = &cobra.Command{
	Use:   "view",
	Short: "View the entire configuration",
	Long:  "Display the complete client configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		configPath := config.GetConfigPath()

		data, err := os.ReadFile(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Println("No configuration file found. Use 'collective init' to create one.")
				return nil
			}
			return fmt.Errorf("failed to read config file: %w", err)
		}

		fmt.Println(string(data))
		return nil
	},
}

var configShowContextCmd = &cobra.Command{
	Use:   "show-context [name]",
	Short: "Show detailed information about a context",
	Long:  "Display detailed information about a context including certificate status",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadClientConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		var contextName string
		if len(args) > 0 {
			contextName = args[0]
		} else {
			contextName = cfg.CurrentContext
		}

		if contextName == "" {
			return fmt.Errorf("no context specified and no current context set")
		}

		ctx, err := cfg.GetContext(contextName)
		if err != nil {
			return err
		}

		// Display context information
		fmt.Printf("Context: %s\n", ctx.Name)
		if ctx.Name == cfg.CurrentContext {
			fmt.Println("Status: CURRENT")
		} else {
			fmt.Println("Status: Available")
		}
		fmt.Println()

		// Connection information
		fmt.Println("CONNECTION DETAILS:")
		fmt.Printf("  Coordinator: %s\n", ctx.Coordinator)
		fmt.Printf("  Member ID:   %s\n", ctx.MemberID)
		if ctx.Description != "" {
			fmt.Printf("  Description: %s\n", ctx.Description)
		}
		fmt.Println()

		// Authentication information
		fmt.Println("AUTHENTICATION:")
		fmt.Printf("  Type: %s\n", ctx.Auth.Type)
		if ctx.Auth.Insecure {
			fmt.Println("  ⚠️  WARNING: Insecure mode (no TLS verification)")
		} else {
			// Check certificates
			fmt.Println("\n  CERTIFICATES:")

			// CA Certificate
			if ctx.Auth.CAPath != "" {
				fmt.Printf("  CA Certificate: %s\n", ctx.Auth.CAPath)
				if _, err := os.Stat(ctx.Auth.CAPath); os.IsNotExist(err) {
					fmt.Println("    Status: ❌ NOT FOUND")
				} else {
					// Load and check CA cert
					if info, err := auth.LoadCertificateInfo(ctx.Auth.CAPath); err == nil {
						fmt.Printf("    Status: ✅ Valid\n")
						fmt.Printf("    Issuer: %s\n", info.Issuer)
						fmt.Printf("    Valid Until: %s\n", info.NotAfter.Format("2006-01-02 15:04:05"))
					} else {
						fmt.Printf("    Status: ⚠️  Error: %v\n", err)
					}
				}
			}

			// Client Certificate
			if ctx.Auth.CertPath != "" {
				fmt.Printf("\n  Client Certificate: %s\n", ctx.Auth.CertPath)
				if _, err := os.Stat(ctx.Auth.CertPath); os.IsNotExist(err) {
					fmt.Println("    Status: ❌ NOT FOUND")
				} else {
					// Load and check client cert
					if info, err := auth.LoadCertificateInfo(ctx.Auth.CertPath); err == nil {
						if info.IsExpired {
							fmt.Printf("    Status: ❌ EXPIRED\n")
						} else if info.ExpiresIn < 30*24*time.Hour {
							fmt.Printf("    Status: ⚠️  EXPIRING SOON (in %d days)\n", int(info.ExpiresIn.Hours()/24))
						} else {
							fmt.Printf("    Status: ✅ Valid\n")
						}
						fmt.Printf("    Subject: %s\n", info.Subject)
						fmt.Printf("    Valid From: %s\n", info.NotBefore.Format("2006-01-02 15:04:05"))
						fmt.Printf("    Valid Until: %s\n", info.NotAfter.Format("2006-01-02 15:04:05"))
						if info.ComponentType != "" {
							fmt.Printf("    Type: %s\n", info.ComponentType)
						}
					} else {
						fmt.Printf("    Status: ⚠️  Error: %v\n", err)
					}
				}
			}

			// Client Key
			if ctx.Auth.KeyPath != "" {
				fmt.Printf("\n  Client Key: %s\n", ctx.Auth.KeyPath)
				if _, err := os.Stat(ctx.Auth.KeyPath); os.IsNotExist(err) {
					fmt.Println("    Status: ❌ NOT FOUND")
				} else {
					info, _ := os.Stat(ctx.Auth.KeyPath)
					fmt.Printf("    Status: ✅ Present\n")
					fmt.Printf("    Permissions: %s\n", info.Mode().String())
					if info.Mode()&0077 != 0 {
						fmt.Println("    ⚠️  WARNING: Key file has overly permissive permissions")
					}
				}
			}

			// Verify certificate chain
			if ctx.Auth.CertPath != "" && ctx.Auth.CAPath != "" {
				fmt.Println("\n  CERTIFICATE CHAIN:")
				if err := auth.VerifyCertificateChain(ctx.Auth.CertPath, ctx.Auth.CAPath); err == nil {
					fmt.Println("    ✅ Certificate chain is valid")
				} else {
					fmt.Printf("    ❌ Certificate chain verification failed: %v\n", err)
				}
			}
		}

		// Test connection
		fmt.Println("\nCONNECTION TEST:")
		connConfig, err := cfg.GetConnectionConfig(contextName)
		if err != nil {
			fmt.Printf("  ❌ Failed to get connection config: %v\n", err)
			return nil
		}

		// Try to connect
		var dialOpts []grpc.DialOption
		if connConfig.Insecure {
			dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
		} else if connConfig.CertPath != "" && connConfig.KeyPath != "" {
			cert, err := tls.LoadX509KeyPair(connConfig.CertPath, connConfig.KeyPath)
			if err != nil {
				fmt.Printf("  ❌ Failed to load certificates: %v\n", err)
				return nil
			}

			tlsConfig := &tls.Config{
				Certificates: []tls.Certificate{cert},
			}

			dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
		}

		conn, err := grpc.Dial(connConfig.Coordinator, dialOpts...)
		if err != nil {
			fmt.Printf("  ❌ Failed to connect: %v\n", err)
		} else {
			defer conn.Close()
			client := protocol.NewCoordinatorClient(conn)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			if _, err := client.GetStatus(ctx, &protocol.GetStatusRequest{}); err != nil {
				fmt.Printf("  ❌ Connection test failed: %v\n", err)
			} else {
				fmt.Println("  ✅ Successfully connected to coordinator")
			}
		}

		return nil
	},
}

func configCommand() *cobra.Command {
	// Add subcommands
	configCmd.AddCommand(configGetContextsCmd)
	configCmd.AddCommand(configCurrentContextCmd)
	configCmd.AddCommand(configUseContextCmd)
	configCmd.AddCommand(configDeleteContextCmd)
	configCmd.AddCommand(configShowContextCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configViewCmd)

	// Add flags
	configDeleteContextCmd.Flags().BoolP("force", "f", false, "Skip confirmation")

	return configCmd
}
